#!/bin/bash
# Rescate de las entradas de LISTA DE ESPERA que quedaron 'unreachable' por no saber entregarle a
# un paciente de número oculto (identificador whatsappusername en vez de teléfono E.164).
#
# EL PROBLEMA (hallazgo H149-1): cuando la corrida diaria encontraba un cupo para uno de estos
# pacientes, el envío del template fallaba y se clasificaba PERMANENTE → la entrada quedaba
# 'unreachable'. Ese estado la saca del pool PARA SIEMPRE: las tres consultas que arman el pool
# (GetDistinctWaitingCups, GetWaitingByCups, GetWaitingByCupsIn en internal/repository/local/
# waiting_list_repo.go) filtran status='waiting' y ninguna rescata 'unreachable'. El paciente sigue
# viendo su entrada, pero el sistema ya no le va a ofrecer un cupo nunca más.
#
# POR QUÉ AHORA SÍ SE PUEDEN RESCATAR: el fix de entrega por BSUID (H148/H149) ya sabe alcanzar a
# estos contactos — está validado en vivo para mensajes de sesión. Al desplegarlo, los pacientes que
# quedaron marcados son los únicos que NO se benefician, porque su entrada ya salió del pool.
#
# QUÉ HACE: devuelve a 'waiting' SOLO las entradas 'unreachable' cuyo phone_number NO es E.164
# (exactamente las víctimas de esta causa) y que aún no han vencido. Limpia notified_at y
# resolved_at para que la entrada vuelva a ser candidata limpia del pool.
# QUÉ NO TOCA: las 'unreachable' con teléfono real (esas fallaron por no tener WhatsApp o por un
# rechazo de Bird sobre un número válido — siguen siendo permanentes de verdad), ni ningún otro
# estado, ni las vencidas.
#
# ORDEN DE EJECUCIÓN: correr DESPUÉS de desplegar el fix de BSUID. Si se corre antes, la corrida
# diaria las volverá a marcar 'unreachable' y no se gana nada (tampoco se pierde: no hay envío
# cobrable, el corte ocurre antes del POST).
#
# Uso:    bash scripts/rescatar-lista-espera-username.sh          # dry-run: muestra a quién rescataría
#         bash scripts/rescatar-lista-espera-username.sh --yes    # ejecuta
# Seguro: aborta si el conteo supera SANITY_MAX (señal de que el filtro pescó de más).
#         Idempotente: re-corrido, encuentra 0 y termina sin tocar nada.

set -euo pipefail
cd "$(dirname "$0")/.."

LOCKDIR="${TMPDIR:-/tmp}/rescate-wl-username.lock.d"
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

SANITY_MAX="${SANITY_MAX:-200}"   # medido: ~1/día desde el 14-ago. Más de 200 = el filtro pesca de más.

# "No E.164" = no cumple ^\+?[0-9]{8,15}$. Es el mismo criterio que usa el bot (utils.IsE164) para
# decidir que el identificador es un username y no un teléfono.
NO_E164="phone_number NOT REGEXP '^\\\\+?[0-9]{8,15}\$'"
# Vigente: sin expires_at, o aún no vencida.
VIGENTE="(expires_at IS NULL OR expires_at > NOW())"
FILTRO="status = 'unreachable' AND $NO_E164 AND $VIGENTE"

APPLY=0; [ "${1:-}" = "--yes" ] && APPLY=1
[ $APPLY -eq 1 ] && echo ">> MODO EJECUCIÓN" || echo ">> MODO VERIFICACIÓN (usa --yes para ejecutar)"
echo ">> BD: $DB_NAME (contenedor $CONTAINER)"
echo

TOTAL_UNREACH=$(Q "SELECT COUNT(*) FROM waiting_list WHERE status='unreachable';")
N=$(Q "SELECT COUNT(*) FROM waiting_list WHERE $FILTRO;")
VENCIDAS=$(Q "SELECT COUNT(*) FROM waiting_list WHERE status='unreachable' AND $NO_E164 AND NOT $VIGENTE;")

echo "unreachable en total ............. $TOTAL_UNREACH"
echo "  de número oculto, vigentes ..... $N   <- a rescatar"
echo "  de número oculto, ya vencidas .. $VENCIDAS  (no se tocan: el cupo que esperaban ya no aplica)"
echo "  con teléfono real .............. $((TOTAL_UNREACH - N - VENCIDAS))  (no se tocan: permanentes de verdad)"
echo

if [ "$N" -eq 0 ]; then
  echo ">> Nada que rescatar. Fin."
  exit 0
fi
if [ "$N" -gt "$SANITY_MAX" ]; then
  echo "ERROR: $N supera SANITY_MAX=$SANITY_MAX. Revisar el filtro antes de ejecutar." >&2
  exit 2
fi

echo ">> Entradas a rescatar (teléfono enmascarado):"
QT "SELECT id,
        CONCAT(LEFT(phone_number,4), '***', RIGHT(phone_number,4)) AS contacto,
        cups_code, cups_name, created_at, notified_at, resolved_at, expires_at
    FROM waiting_list WHERE $FILTRO ORDER BY created_at;"
echo

if [ $APPLY -eq 0 ]; then
  echo ">> DRY-RUN: no se modificó nada. Ejecuta con --yes (después de desplegar el fix de BSUID)."
  exit 0
fi

Q "UPDATE waiting_list
     SET status = 'waiting', notified_at = NULL, resolved_at = NULL, updated_at = NOW()
   WHERE $FILTRO;"

RESTAN=$(Q "SELECT COUNT(*) FROM waiting_list WHERE $FILTRO;")
echo ">> Rescatadas: $N. Quedan con el filtro: $RESTAN (debe ser 0)."
echo ">> Verificación posterior: en la próxima corrida diaria, buscar en los logs"
echo "   'delivered_template_via_bsuid' para estos contactos (entrega efectiva), o"
echo "   'bsuid_template_send_failed' si Bird no admite templates al BSUID."
[ "$RESTAN" -eq 0 ] || { echo "ERROR: el UPDATE no cubrió todo." >&2; exit 4; }
