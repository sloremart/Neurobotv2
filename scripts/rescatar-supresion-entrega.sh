#!/bin/bash
# Rescate de los teléfonos que quedaron SUPRIMIDOS por fallos de entrega y ya no pueden volver solos.
#
# EL PROBLEMA (hallazgo H150-2): con >=2 fallos consecutivos, notifications.Deliverable() devuelve
# false y los templates programados a ese número se SUPRIMEN (scheduler/tasks.go:224 y :402, y la
# lista de espera en waiting_list_check.go:380). Como deja de salir cualquier saliente, ya no puede
# llegar ningún delivered/read — que era el único disparador de RecordSuccess. El contador se queda
# clavado y el paciente pierde sus recordatorios de cita, en silencio, para siempre.
#
# YA CORREGIDO PARA EL FUTURO: un mensaje ENTRANTE del paciente ahora resetea el contador
# (webhook_handler.recordInboundDeliveryProof). Pero eso solo salva a quien escribe. Los que solo
# RECIBEN recordatorios y nunca escriben —justo el caso que este mecanismo perjudica— siguen
# atrapados: para esos es este script.
#
# QUÉ HACE, Y POR QUÉ ASÍ: NO pone el contador a 0, lo pone a 1 (deliveryFailureThreshold - 1).
# Con Bird cada envío se cobra aunque no se entregue, así que el rescate se diseña al coste mínimo:
#   - a 1, el número queda JUSTO por debajo del umbral → sale UN template de prueba, no más;
#   - si ese envío vuelve a fallar, el contador sube a 2 y el número se re-suprime solo. Coste
#     total de rescatar un número realmente muerto: 1 mensaje, una vez.
#   - a 0 harían falta DOS fallos para re-suprimirlo: el doble de coste por el mismo diagnóstico.
#
# A QUIÉN NO TOCA: los que están fallando AHORA MISMO (last_failure_at reciente, ver DIAS_QUIETO):
# reintentar contra un número que acaba de fallar es pagar por el mismo resultado.
#
# Uso:    bash scripts/rescatar-supresion-entrega.sh          # dry-run: muestra a quién rescataría
#         bash scripts/rescatar-supresion-entrega.sh --yes    # ejecuta
# Ajustes: DIAS_QUIETO=7 (días desde el último fallo)  ·  SANITY_MAX=200 (freno si pesca de más)
# Seguro:  idempotente (re-corrido encuentra 0), con candado, teléfonos enmascarados en pantalla.

set -euo pipefail
cd "$(dirname "$0")/.."

LOCKDIR="${TMPDIR:-/tmp}/rescate-supresion-entrega.lock.d"
if ! mkdir "$LOCKDIR" 2>/dev/null; then
  echo "ERROR: otra instancia ya está corriendo (o candado huérfano: rmdir $LOCKDIR)." >&2
  exit 3
fi
trap 'rmdir "$LOCKDIR" 2>/dev/null' EXIT

ENV_FILE="${ENV_FILE:-.env}"
CONTAINER="${DB_CONTAINER:-neuro_bot_db}"
getenv(){ grep -E "^$1=" "$ENV_FILE" 2>/dev/null | head -1 | sed -E 's/^[^=]+=//; s/[[:space:]]+#.*$//; s/[[:space:]]*$//' || true; }
DB_NAME="${DB_NAME_OVERRIDE:-$(getenv DB_DATABASE)}"; DB_NAME="${DB_NAME:-neuro_bot}"
DB_ROOT="$(docker exec "$CONTAINER" sh -c 'printf %s "$MYSQL_ROOT_PASSWORD"' 2>/dev/null || true)"
[ -z "$DB_ROOT" ] && DB_ROOT="$(getenv DB_ROOT_PASSWORD)"
[ -z "$DB_ROOT" ] && { echo "ERROR: sin contraseña de root (ni contenedor $CONTAINER ni $ENV_FILE)" >&2; exit 1; }

Q(){ docker exec -e MYSQL_PWD="$DB_ROOT" "$CONTAINER" mysql -uroot -N -e "$1" "$DB_NAME"; }
QT(){ docker exec -e MYSQL_PWD="$DB_ROOT" "$CONTAINER" mysql -uroot -t -e "$1" "$DB_NAME"; }

UMBRAL=2                              # deliveryFailureThreshold (internal/notifications/deliverability.go)
SONDA=$((UMBRAL - 1))                 # valor de rescate: justo por debajo del umbral = UN envío de prueba
DIAS_QUIETO="${DIAS_QUIETO:-7}"
SANITY_MAX="${SANITY_MAX:-200}"       # medido 11-25 ago: 11 pacientes. Más de 200 = revisar el filtro.

QUIETO="(last_failure_at IS NULL OR last_failure_at < NOW() - INTERVAL $DIAS_QUIETO DAY)"
FILTRO="consecutive_failures >= $UMBRAL AND $QUIETO"

APPLY=0; [ "${1:-}" = "--yes" ] && APPLY=1
[ $APPLY -eq 1 ] && echo ">> MODO EJECUCIÓN" || echo ">> MODO VERIFICACIÓN (usa --yes para ejecutar)"
echo ">> BD: $DB_NAME (contenedor $CONTAINER) · umbral=$UMBRAL · sonda=$SONDA · quieto>=${DIAS_QUIETO}d"
echo

TOTAL_SUP=$(Q "SELECT COUNT(*) FROM message_delivery_failures WHERE consecutive_failures >= $UMBRAL;")
N=$(Q "SELECT COUNT(*) FROM message_delivery_failures WHERE $FILTRO;")

echo "suprimidos en total ............. $TOTAL_SUP"
echo "  quietos >= ${DIAS_QUIETO}d ............... $N   <- a rescatar (1 template de prueba cada uno)"
echo "  con fallo reciente ............. $((TOTAL_SUP - N))  (no se tocan: reintentar ahora es pagar por el mismo fallo)"
echo

if [ "$N" -eq 0 ]; then
  echo ">> Nada que rescatar. Fin."
  exit 0
fi
if [ "$N" -gt "$SANITY_MAX" ]; then
  echo "ERROR: $N supera SANITY_MAX=$SANITY_MAX. Revisar el filtro antes de ejecutar." >&2
  exit 2
fi

echo ">> Teléfonos a rescatar (enmascarados):"
QT "SELECT CONCAT(LEFT(phone_number,4), '***', RIGHT(phone_number,4)) AS contacto,
           consecutive_failures AS fallos, last_status, last_failure_at
    FROM message_delivery_failures WHERE $FILTRO ORDER BY last_failure_at;"
echo
echo ">> Coste máximo de este rescate: $N mensaje(s) cobrado(s) — uno por número. Los que sigan"
echo "   muertos se re-suprimen solos al primer fallo."
echo

if [ $APPLY -eq 0 ]; then
  echo ">> DRY-RUN: no se modificó nada. Ejecuta con --yes."
  exit 0
fi

Q "UPDATE message_delivery_failures
     SET consecutive_failures = $SONDA, last_status = 'rescued', updated_at = NOW()
   WHERE $FILTRO;"

RESTAN=$(Q "SELECT COUNT(*) FROM message_delivery_failures WHERE $FILTRO;")
echo ">> Rescatados: $N. Quedan con el filtro: $RESTAN (debe ser 0)."
echo ">> Verificación posterior: en la próxima corrida de recordatorios, buscar en los logs"
echo "   'skip reminder - entregas de WhatsApp fallando' para estos números (siguen muertos) o"
echo "   su ausencia + un 'delivery failure recorded'/'delivered' (el envío de prueba salió)."
[ "$RESTAN" -eq 0 ] || { echo "ERROR: el UPDATE no cubrió todo." >&2; exit 4; }
