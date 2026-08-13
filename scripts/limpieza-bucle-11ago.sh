#!/bin/bash
# Limpieza de los datos falsos que dejó el BUCLE no-show↔re-escalación del 11/12-ago-2026
# (incidente H138-1, fix desplegado 12-ago 10:49 en 72860ec) en la BD LOCAL DEL BOT (neuro_bot).
#
# El bucle: ~18 sesiones re-escalando 1 vez POR MINUTO entre el 11-ago 18:13 y el deploy del
# 12-ago 10:49. Cada vuelta dejó: flow_events (cups_none|bloqueo_sanitas_escalated + escalated +
# agent_no_show), UNA fila en escalations (Create por re-escalación) y chat_events
# (escalated_to_agent / escalation_agent_no_show). Eso envenena los funnels de agendar/escalación
# y el módulo SLA del dashboard para esos 2 días.
#
# IDENTIFICACIÓN AUTO-CONTENIDA (no hay lista fija de traces): dentro de la ventana, una sesión
# LEGÍTIMA produce 1-3 eventos de esos steps; una del bucle produce CIENTOS (1/min). El umbral
# UMBRAL_VUELTAS=30 separa con margen enorme ambos mundos. El dry-run imprime la lista exacta
# de traces detectados con sus conteos ANTES de tocar nada.
#
# Borra EXCLUSIVAMENTE, para los traces detectados y DENTRO de la ventana:
#   A) flow_events de los steps del bucle (la escalación ORIGINAL de cada sesión es previa a la
#      ventana y no se toca).
#   B) escalations con created_at en la ventana (ídem: la fila original es pre-ventana).
#   C) chat_events escalated_to_agent / escalation_agent_no_show en la ventana.
#   D) Recalcula el rollup flow_daily_stats del 2026-08-11 y 2026-08-12 desde los crudos limpios.
# NO toca: sessions, SIESA, ni ningún evento fuera de la ventana o de otros traces.
#
# Uso:    bash scripts/limpieza-bucle-11ago.sh          # dry-run: muestra qué haría
#         bash scripts/limpieza-bucle-11ago.sh --yes    # ejecuta
# Seguro: aborta si detecta <5 o >40 traces (fuera del rango plausible del incidente) o si el
#         total a borrar supera SANITY_MAX. Idempotente: re-corrido tras limpiar, detecta 0
#         traces y termina sin tocar nada.
# Validado E2E contra una tormenta sintética que replica el patrón exacto (2 traces de bucle +
# sesiones legítimas) — ver el commit de este script.

set -euo pipefail
cd "$(dirname "$0")/.."

LOCKDIR="${TMPDIR:-/tmp}/limpieza-bucle-11ago.lock.d"
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

VENTANA_INI='2026-08-11 18:05:00'   # el bucle arrancó 18:13; margen por relojes
VENTANA_FIN='2026-08-12 10:50:00'   # deploy del fix: 10:49
STEPS_BUCLE="'cups_none','bloqueo_sanitas_escalated','escalated','agent_no_show'"
UMBRAL_VUELTAS=30                    # legítimo: 1-3 eventos de estos steps; bucle: cientos
SANITY_MAX=80000                     # tope duro de filas a borrar en flow_events (medido: ~20-25k)
LOTE=50000

APPLY=0; [ "${1:-}" = "--yes" ] && APPLY=1
[ $APPLY -eq 1 ] && echo ">> MODO EJECUCIÓN" || echo ">> MODO VERIFICACIÓN (usa --yes para ejecutar)"

# ── 0) Detectar los traces del bucle (auto-identificación por repetición patológica) ────────
TRACES=$(Q "SELECT trace_id FROM flow_events
            WHERE created_at >= '$VENTANA_INI' AND created_at <= '$VENTANA_FIN'
              AND step IN ($STEPS_BUCLE) AND trace_id LIKE 'sess:%'
            GROUP BY trace_id HAVING COUNT(*) >= $UMBRAL_VUELTAS;")
NTR=$(echo "$TRACES" | grep -c . || true)
echo ">> 0) traces de bucle detectados: $NTR (umbral: >=$UMBRAL_VUELTAS eventos en la ventana)"
if [ "$NTR" -eq 0 ]; then
  echo "   Nada que limpiar (ya limpio o los crudos expiraron). Fin."
  exit 0
fi
if [ "$NTR" -lt 5 ] || [ "$NTR" -gt 40 ]; then
  echo "ERROR: $NTR traces está fuera del rango plausible del incidente (5-40) — revisar a mano." >&2
  exit 2
fi
IN_TRACES=$(echo "$TRACES" | sed "s/^/'/; s/$/'/" | paste -sd, -)
SESS_IDS=$(echo "$TRACES" | sed "s/^sess://; s/^/'/; s/$/'/" | paste -sd, -)

echo ">> detalle por trace (eventos de bucle en ventana):"
Q "SELECT trace_id, COUNT(*) FROM flow_events
   WHERE created_at >= '$VENTANA_INI' AND created_at <= '$VENTANA_FIN'
     AND step IN ($STEPS_BUCLE) AND trace_id IN ($IN_TRACES)
   GROUP BY trace_id ORDER BY COUNT(*) DESC;"

# ── A) flow_events del bucle ────────────────────────────────────────────────────────────────
NFE=$(Q "SELECT COUNT(*) FROM flow_events
         WHERE created_at >= '$VENTANA_INI' AND created_at <= '$VENTANA_FIN'
           AND step IN ($STEPS_BUCLE) AND trace_id IN ($IN_TRACES);")
echo ">> A) flow_events a borrar: $NFE"
[ "$NFE" -gt "$SANITY_MAX" ] && { echo "ERROR: $NFE supera SANITY_MAX=$SANITY_MAX — revisar a mano." >&2; exit 2; }

# ── B) escalations falsas (una fila por vuelta del bucle) ───────────────────────────────────
NESC=$(Q "SELECT COUNT(*) FROM escalations
          WHERE created_at >= '$VENTANA_INI' AND created_at <= '$VENTANA_FIN'
            AND session_id IN ($SESS_IDS);")
echo ">> B) escalations a borrar: $NESC (la fila ORIGINAL de cada sesión es pre-ventana y se conserva)"

# ── C) chat_events del bucle ────────────────────────────────────────────────────────────────
NCE=$(Q "SELECT COUNT(*) FROM chat_events
         WHERE created_at >= '$VENTANA_INI' AND created_at <= '$VENTANA_FIN'
           AND session_id IN ($SESS_IDS)
           AND event_type IN ('escalated_to_agent','escalation_agent_no_show');")
echo ">> C) chat_events a borrar: $NCE"

if [ $APPLY -eq 0 ]; then
  echo ">> DRY-RUN terminado. Revisa la lista de traces y los conteos; ejecuta con --yes."
  exit 0
fi

# ── EJECUCIÓN (por lotes en flow_events, que es la tabla grande) ────────────────────────────
START=$(date +%s); TOTAL=0
while true; do
  N=$(Q "DELETE FROM flow_events
         WHERE created_at >= '$VENTANA_INI' AND created_at <= '$VENTANA_FIN'
           AND step IN ($STEPS_BUCLE) AND trace_id IN ($IN_TRACES) LIMIT $LOTE;
         SELECT ROW_COUNT();" | tail -1)
  TOTAL=$((TOTAL+N)); [ "$N" -eq 0 ] && break
  echo "   A) lote de $N (acumulado $TOTAL)"
done
echo ">> A) borradas $TOTAL filas de flow_events en $(( $(date +%s) - START ))s."

Q "DELETE FROM escalations
   WHERE created_at >= '$VENTANA_INI' AND created_at <= '$VENTANA_FIN'
     AND session_id IN ($SESS_IDS);"
echo ">> B) escalations del bucle borradas."

Q "DELETE FROM chat_events
   WHERE created_at >= '$VENTANA_INI' AND created_at <= '$VENTANA_FIN'
     AND session_id IN ($SESS_IDS)
     AND event_type IN ('escalated_to_agent','escalation_agent_no_show');"
echo ">> C) chat_events del bucle borrados."

# ── D) Recalcular rollup de los 2 días desde los crudos limpios ─────────────────────────────
Q "DELETE FROM flow_daily_stats WHERE day IN ('2026-08-11','2026-08-12');
   INSERT INTO flow_daily_stats (day, flow, step, outcome, reason, cnt)
   SELECT DATE(created_at), flow, step, outcome, COALESCE(reason,''), COUNT(*)
   FROM flow_events
   WHERE created_at >= '2026-08-11 00:00:00' AND created_at < '2026-08-13 00:00:00'
   GROUP BY DATE(created_at), flow, step, outcome, COALESCE(reason,'');"
echo ">> D) rollup del 2026-08-11 y 2026-08-12 recalculado."

echo ">> VERIFICACIÓN FINAL (todo debe dar 0):"
Q "SELECT 'fe_restantes', COUNT(*) FROM flow_events
   WHERE created_at >= '$VENTANA_INI' AND created_at <= '$VENTANA_FIN'
     AND step IN ($STEPS_BUCLE) AND trace_id IN ($IN_TRACES)
   UNION ALL SELECT 'esc_restantes', COUNT(*) FROM escalations
   WHERE created_at >= '$VENTANA_INI' AND created_at <= '$VENTANA_FIN' AND session_id IN ($SESS_IDS)
   UNION ALL SELECT 'ce_restantes', COUNT(*) FROM chat_events
   WHERE created_at >= '$VENTANA_INI' AND created_at <= '$VENTANA_FIN' AND session_id IN ($SESS_IDS)
     AND event_type IN ('escalated_to_agent','escalation_agent_no_show');"
echo ">> cups_none del 11-ago en el rollup limpio (debe quedar en cifras de día normal, ~50-100):"
Q "SELECT day, cnt FROM flow_daily_stats WHERE flow='agendar' AND step='cups_none' AND day IN ('2026-08-11','2026-08-12');"
echo ">> Listo. Nota: 'OPTIMIZE TABLE flow_events;' es opcional (horario valle)."
