#!/bin/bash
# Limpieza de los datos falsos que dejó el incidente del 28-jul-2026 (recursión "Asesor")
# en la BD LOCAL DEL BOT (neuro_bot, MySQL). Ver docs/INCIDENTE-28JUL-LIMPIEZA-DATOS.md.
#
# Borra EXCLUSIVAMENTE:
#   A) 78 flow_events de la traza colateral (19 patient_created + 59 contract_resolved duplicados,
#      conservando 1 de cada — el registro real ocurrió).
#   B) ~10.32M flow_events ai_recovery_started de la traza-bomba (conservando el primero, que fue
#      el intento real de recuperación), POR LOTES para no bloquear la tabla.
#   C) Recalcula el rollup flow_daily_stats del 2026-07-28 desde los crudos ya limpios.
# NO toca: chat_events, sessions, escalations, SIESA (verificado sin daño).
#
# Uso:    bash scripts/limpieza-incidente-28jul.sh --yes
# Seguro: sin --yes solo VERIFICA y muestra lo que haría. Aborta si los conteos no coinciden
#         con lo esperado (validado en la réplica local del dump de prod del 04-ago).
#         Idempotente: si una sección ya está limpia, la salta.

set -euo pipefail
cd "$(dirname "$0")/.."

ENV_FILE="${ENV_FILE:-.env}"
CONTAINER="${DB_CONTAINER:-neuro_bot_db}"
getenv(){ grep -E "^$1=" "$ENV_FILE" 2>/dev/null | head -1 | sed -E 's/^[^=]+=//; s/[[:space:]]+#.*$//; s/[[:space:]]*$//'; }
DB_NAME="$(getenv DB_DATABASE)"; DB_NAME="${DB_NAME:-neuro_bot}"
DB_ROOT="$(getenv DB_ROOT_PASSWORD)"
[ -z "$DB_ROOT" ] && { echo "ERROR: no pude leer DB_ROOT_PASSWORD de $ENV_FILE" >&2; exit 1; }

Q(){ docker exec -e MYSQL_PWD="$DB_ROOT" "$CONTAINER" mysql -uroot -N -e "$1" "$DB_NAME"; }

TR_COLATERAL='sess:005d66e4-894f-4cae-ab57-398427d18331'
TR_BOMBA='sess:002b2acb-b57d-4834-a2b8-c765cab0bd28'
VENTANA_INI='2026-07-28 09:58:50'; VENTANA_FIN='2026-07-28 10:30:15'
LOTE=200000

APPLY=0; [ "${1:-}" = "--yes" ] && APPLY=1
[ $APPLY -eq 1 ] && echo ">> MODO EJECUCIÓN" || echo ">> MODO VERIFICACIÓN (usa --yes para ejecutar)"

# ── A) Traza colateral: 19 patient_created + 59 contract_resolved duplicados ────────────────
PC=$(Q "SELECT COUNT(*) FROM flow_events WHERE trace_id='$TR_COLATERAL' AND step='patient_created'
        AND created_at >= '$VENTANA_INI' AND created_at <= '$VENTANA_FIN';")
CR=$(Q "SELECT COUNT(*) FROM flow_events WHERE trace_id='$TR_COLATERAL' AND step='contract_resolved'
        AND created_at >= '$VENTANA_INI' AND created_at <= '$VENTANA_FIN';")
echo ">> A) colateral: patient_created=$PC contract_resolved=$CR (esperado 20/60; limpio 1/1)"
if [ "$PC" -le 1 ] && [ "$CR" -le 1 ]; then
  echo "   A) ya está limpia — se salta."
elif [ "$PC" -eq 20 ] && [ "$CR" -eq 60 ]; then
  if [ $APPLY -eq 1 ]; then
    KEEP_PC=$(Q "SELECT MIN(id) FROM flow_events WHERE trace_id='$TR_COLATERAL' AND step='patient_created';")
    KEEP_CR=$(Q "SELECT MIN(id) FROM flow_events WHERE trace_id='$TR_COLATERAL' AND step='contract_resolved';")
    Q "DELETE FROM flow_events WHERE trace_id='$TR_COLATERAL'
       AND created_at >= '$VENTANA_INI' AND created_at <= '$VENTANA_FIN'
       AND step IN ('patient_created','contract_resolved') AND id NOT IN ($KEEP_PC,$KEEP_CR);"
    echo "   A) borradas 78 filas (quedó 1 de cada)."
  fi
else
  echo "ERROR: conteos inesperados en A) — revisar a mano antes de continuar." >&2; exit 2
fi

# ── B) Traza-bomba: ~10.32M ai_recovery_started (conservar el primero) ──────────────────────
NB=$(Q "SELECT COUNT(*) FROM flow_events WHERE trace_id='$TR_BOMBA' AND step='ai_recovery_started';")
echo ">> B) bomba: ai_recovery_started=$NB (esperado 10319979; limpio 1)"
if [ "$NB" -le 1 ]; then
  echo "   B) ya está limpia — se salta."
elif [ "$NB" -eq 10319979 ]; then
  if [ $APPLY -eq 1 ]; then
    KEEP=$(Q "SELECT MIN(id) FROM flow_events WHERE trace_id='$TR_BOMBA' AND step='ai_recovery_started';")
    START=$(date +%s); TOTAL=0
    while true; do
      N=$(Q "DELETE FROM flow_events WHERE trace_id='$TR_BOMBA' AND step='ai_recovery_started'
             AND id <> $KEEP LIMIT $LOTE; SELECT ROW_COUNT();" | tail -1)
      TOTAL=$((TOTAL+N)); [ "$N" -eq 0 ] && break
      echo "   B) lote de $N (acumulado $TOTAL)"
    done
    echo "   B) borradas $TOTAL filas en $(( $(date +%s) - START ))s."
  fi
else
  echo "ERROR: conteo inesperado en B) — revisar a mano antes de continuar." >&2; exit 2
fi

# ── C) Recalcular rollup del 28-jul desde los crudos limpios ────────────────────────────────
if [ $APPLY -eq 1 ]; then
  Q "DELETE FROM flow_daily_stats WHERE day='2026-07-28';
     INSERT INTO flow_daily_stats (day, flow, step, outcome, reason, cnt)
     SELECT DATE(created_at), flow, step, outcome, COALESCE(reason,''), COUNT(*)
     FROM flow_events
     WHERE created_at >= '2026-07-28 00:00:00' AND created_at < '2026-07-29 00:00:00'
     GROUP BY DATE(created_at), flow, step, outcome, COALESCE(reason,'');"
  echo ">> C) rollup del 2026-07-28 recalculado."

  # Rollup: started=11 porque el día tuvo 10 intentos reales de OTROS pacientes + el 1 conservado.
  echo ">> VERIFICACIÓN FINAL (esperado: pc=1 cr=1 started=1; rollup started=11, pc=87, cr=359):"
  Q "SELECT 'pc', COUNT(*) FROM flow_events WHERE trace_id='$TR_COLATERAL' AND step='patient_created'
     UNION ALL SELECT 'cr', COUNT(*) FROM flow_events WHERE trace_id='$TR_COLATERAL' AND step='contract_resolved'
     UNION ALL SELECT 'started', COUNT(*) FROM flow_events WHERE trace_id='$TR_BOMBA' AND step='ai_recovery_started';"
  Q "SELECT step, cnt FROM flow_daily_stats WHERE day='2026-07-28'
     AND ((flow='recuperacion' AND step='ai_recovery_started')
       OR (flow='registro' AND step='patient_created')
       OR (flow='entidad' AND step='contract_resolved'));"
  echo ">> Listo. Total flow_events ahora:"
  Q "SELECT COUNT(*) FROM flow_events;"
  echo ">> Nota: InnoDB reutiliza el espacio liberado; 'OPTIMIZE TABLE flow_events;' es opcional (horario valle)."
fi
