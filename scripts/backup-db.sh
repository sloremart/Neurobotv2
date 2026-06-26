#!/bin/bash
# Backup de la BD interna del bot (neuro_bot)
# Uso: ./scripts/backup-db.sh
# Genera: backups/neuro_bot_YYYY-MM-DD_HHMMSS.sql.gz
#
# Crontab (backup diario a las 3am):
#   crontab -e
#   0 3 * * * /ruta/al/proyecto/neuro-bot/scripts/backup-db.sh >> /ruta/al/proyecto/neuro-bot/backups/cron.log 2>&1

set -euo pipefail

CONTAINER="neuro_bot_db"
DB_NAME="${DB_DATABASE:-neuro_bot}"
DB_USER="${DB_USER:-botuser}"
DB_PASS="${DB_PASSWORD:-botpass}"
BACKUP_DIR="$(cd "$(dirname "$0")/.." && pwd)/backups"

mkdir -p "$BACKUP_DIR"

TIMESTAMP=$(date +%Y-%m-%d_%H%M%S)
FILENAME="neuro_bot_${TIMESTAMP}.sql.gz"

echo "Backing up ${DB_NAME} from ${CONTAINER}..."

docker exec "$CONTAINER" \
  mysqldump -u"$DB_USER" -p"$DB_PASS" \
  --single-transaction --routines --triggers --no-tablespaces \
  "$DB_NAME" | gzip > "${BACKUP_DIR}/${FILENAME}"

SIZE=$(du -h "${BACKUP_DIR}/${FILENAME}" | cut -f1)
echo "Backup saved: backups/${FILENAME} (${SIZE})"

# Limpiar backups > 30 dias
find "$BACKUP_DIR" -name "neuro_bot_*.sql.gz" -mtime +30 -delete 2>/dev/null || true
echo "Old backups (>30 days) cleaned."
