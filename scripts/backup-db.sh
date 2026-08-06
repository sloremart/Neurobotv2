#!/bin/bash
# Backup de la BD interna del bot (neuro_bot)
# Uso: ./scripts/backup-db.sh
# Genera: backups/neuro_bot_YYYY-MM-DD_HHMMSS.sql.gz
#
# Credenciales: se leen del .env del proyecto (igual que restore-local-db.sh), así que basta con
# EJECUTARLO — no hay que exportar nada. Precedencia: variables de entorno > .env > defaults de dev.
# El .env se busca junto al proyecto, NO en el directorio actual, para que el cron funcione sin `cd`.
#
# Crontab (backup diario a las 3am):
#   crontab -e
#   0 3 * * * /ruta/al/proyecto/neuro-bot/scripts/backup-db.sh >> /ruta/al/proyecto/neuro-bot/backups/cron.log 2>&1
#
# NO respalda SIESA (ZeusSalud_Neuro, SQL Server): esa es externa y la respalda sistemas de la IPS.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CONTAINER="${DB_CONTAINER:-neuro_bot_db}"
BACKUP_DIR="${ROOT}/backups"
ENV_FILE="${ENV_FILE:-${ROOT}/.env}"

# getenv: extrae una var del .env sin ejecutarlo (tolera comentarios inline). Mismo criterio que
# scripts/restore-local-db.sh y auditoria/cycle.sh.
# `|| true`: sin él, con `set -e` un grep sin coincidencia (variable ausente del .env) aborta el
# script ANTES de poder caer al default de abajo — y en el cron eso es un backup que no se hace y
# no deja rastro claro.
getenv(){ grep -E "^$1=" "$ENV_FILE" 2>/dev/null | head -1 | sed -E 's/^[^=]+=//; s/[[:space:]]+#.*$//; s/[[:space:]]*$//' || true; }

DB_NAME="${DB_DATABASE:-$(getenv DB_DATABASE)}"; DB_NAME="${DB_NAME:-neuro_bot}"
DB_USER="${DB_USER:-$(getenv DB_USER)}";         DB_USER="${DB_USER:-botuser}"
DB_PASS="${DB_PASSWORD:-$(getenv DB_PASSWORD)}"; DB_PASS="${DB_PASS:-botpass}"

# Aviso explícito si no hubo de dónde leer: en prod con credenciales propias, correr con los defaults
# de dev falla con "Access denied" y, en un cron, ese fallo solo se ve si alguien lee el log.
if [ ! -f "$ENV_FILE" ]; then
  echo "AVISO: no existe $ENV_FILE; usando credenciales del entorno o los defaults de desarrollo." >&2
fi

mkdir -p "$BACKUP_DIR"

TIMESTAMP=$(date +%Y-%m-%d_%H%M%S)
FILENAME="neuro_bot_${TIMESTAMP}.sql.gz"
DEST="${BACKUP_DIR}/${FILENAME}"

# La redirección crea el .gz ANTES de que corra mysqldump: si el dump falla, quedaría un archivo con
# nombre y fecha perfectos pero vacío o truncado — un backup falso, que es peor que no tener backup.
# El trap lo borra salvo que la verificación final haya pasado.
BACKUP_OK=0
cleanup_partial(){
  if [ "$BACKUP_OK" != "1" ] && [ -f "$DEST" ]; then
    rm -f "$DEST"
    echo "ERROR: el backup falló; se descartó ${FILENAME} para no dejar un archivo engañoso." >&2
  fi
}
trap cleanup_partial EXIT

echo "Backing up ${DB_NAME} from ${CONTAINER} (user: ${DB_USER})..."

docker exec "$CONTAINER" \
  mysqldump -u"$DB_USER" -p"$DB_PASS" \
  --single-transaction --routines --triggers --no-tablespaces \
  "$DB_NAME" | gzip > "$DEST"

# Verificación en UNA pasada: gunzip falla (con pipefail) si el .gz está corrupto o truncado —el caso
# típico de disco lleno— y el conteo detecta el dump vacío (mysqldump que salió 0 sin escribir nada).
BYTES=$(gunzip -c "$DEST" | wc -c)
if [ "$BYTES" -lt 1 ]; then
  echo "ERROR: el dump quedó vacío." >&2
  exit 1
fi
BACKUP_OK=1

SIZE=$(du -h "$DEST" | cut -f1)
echo "Backup saved: backups/${FILENAME} (${SIZE})"

# Limpiar backups > 30 dias
find "$BACKUP_DIR" -name "neuro_bot_*.sql.gz" -mtime +30 -delete 2>/dev/null || true
echo "Old backups (>30 days) cleaned."
