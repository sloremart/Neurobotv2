#!/bin/bash
# ==============================================================================
# Pre-Deploy Validation Script
# Verifica que todo este listo antes de construir Docker.
# Ejecutar desde la raiz del proyecto: ./scripts/pre-deploy-check.sh
# ==============================================================================

set -uo pipefail

# Colores
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color
BOLD='\033[1m'

# Contadores
PASS=0
FAIL=0
WARN=0

pass()  { PASS=$((PASS + 1)); echo -e "  ${GREEN}[OK]${NC} $1"; }
fail()  { FAIL=$((FAIL + 1)); echo -e "  ${RED}[FAIL]${NC} $1"; echo -e "       ${YELLOW}FIX:${NC} $2"; }
warn()  { WARN=$((WARN + 1)); echo -e "  ${YELLOW}[WARN]${NC} $1"; echo -e "       ${CYAN}INFO:${NC} $2"; }
header(){ echo -e "\n${BOLD}=== $1 ===${NC}"; }

# Detectar raiz del proyecto (donde esta docker-compose.yml)
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_DIR"

echo -e "${BOLD}Pre-Deploy Validation - Neuro Bot${NC}"
echo "Directorio: $PROJECT_DIR"
echo "Fecha: $(date)"

# ==========================================================================
# 1. Docker instalado y corriendo
# ==========================================================================
header "1. Docker Engine"

if command -v docker &>/dev/null; then
    DOCKER_VER=$(docker --version 2>/dev/null | head -1)
    pass "Docker instalado: $DOCKER_VER"
else
    fail "Docker no instalado" "curl -fsSL https://get.docker.com | sh && sudo usermod -aG docker \$USER"
fi

if command -v docker &>/dev/null && docker compose version &>/dev/null; then
    COMPOSE_VER=$(docker compose version 2>/dev/null | head -1)
    pass "Docker Compose: $COMPOSE_VER"
else
    fail "Docker Compose no disponible" "Instalar Docker Compose V2 (viene incluido con Docker Desktop o docker-compose-plugin)"
fi

if command -v docker &>/dev/null && docker info &>/dev/null 2>&1; then
    pass "Docker daemon corriendo"
else
    fail "Docker daemon no responde" "sudo systemctl start docker"
fi

# Verificar auto-start al boot (solo Linux con systemd)
if command -v systemctl &>/dev/null; then
    if systemctl is-enabled docker &>/dev/null 2>&1; then
        pass "Docker auto-start al boot (enabled)"
    else
        warn "Docker NO arranca automaticamente al reiniciar el servidor" "sudo systemctl enable docker"
    fi
fi

# ==========================================================================
# 2. Archivos requeridos
# ==========================================================================
header "2. Archivos del Proyecto"

REQUIRED_FILES=(
    "docker-compose.yml"
    "docker/Dockerfile"
    "migrations/024_seed_center_locations.up.sql"
    "go.mod"
    "go.sum"
    "cmd/server/main.go"
)

for f in "${REQUIRED_FILES[@]}"; do
    if [ -f "$f" ]; then
        pass "$f"
    else
        fail "$f no encontrado" "Verificar que el repositorio esta completo (git status)"
    fi
done

# Archivos opcionales de scaling
if [ -f "docker-compose.high-load.yml" ] && [ -f ".env.high-load" ]; then
    pass "Perfil high-load disponible (docker-compose.high-load.yml + .env.high-load)"
else
    warn "Perfil high-load no disponible" "Sin esto no podras escalar a ~1000 chats/hora con ./scripts/scale-up.sh"
fi

if [ -d "migrations" ] && ls migrations/*.sql &>/dev/null 2>&1; then
    MIGRATION_COUNT=$(ls migrations/*.sql 2>/dev/null | wc -l)
    pass "Migraciones: $MIGRATION_COUNT archivos .sql"
else
    fail "Directorio migrations/ vacio o no existe" "El bot necesita migraciones para crear tablas"
fi

# ==========================================================================
# 3. Archivo .env
# ==========================================================================
header "3. Configuracion (.env)"

if [ -f ".env" ]; then
    pass ".env existe"
else
    fail ".env no existe" "cp .env.example .env && nano .env  (llenar todas las variables)"
fi

if [ -f ".env" ]; then
    # Variables criticas que NO pueden estar vacias
    CRITICAL_VARS=(
        "EXTERNAL_DB_USER"
        "EXTERNAL_DB_PASSWORD"
        "BIRD_API_KEY_WA"
        "BIRD_ACCESS_KEY_ID"
        "BIRD_WEBHOOK_SECRET"
        "BIRD_WORKSPACE_ID"
        "BIRD_CHANNEL_ID"
        "BIRD_TEAM_FALLBACK"
        "OPENAI_API_KEY"
        "INTERNAL_API_KEY"
        "NGROK_AUTHTOKEN"
    )

    for var in "${CRITICAL_VARS[@]}"; do
        val=$(grep "^${var}=" .env 2>/dev/null | cut -d'=' -f2- | xargs 2>/dev/null || echo "")
        if [ -n "$val" ] && [ "$val" != '""' ] && [ "$val" != "''" ]; then
            pass "$var configurado"
        else
            fail "$var vacio o no definido" "Editar .env y configurar $var"
        fi
    done

    # Variables recomendadas (warn si vacias)
    RECOMMENDED_VARS=(
        "TG_BOT_TOKEN"
        "TG_CHAT_IDS"
        "TELEGRAM_BOT_TOKEN"
        "TELEGRAM_CHAT_ID"
    )

    for var in "${RECOMMENDED_VARS[@]}"; do
        val=$(grep "^${var}=" .env 2>/dev/null | cut -d'=' -f2- | xargs 2>/dev/null || echo "")
        if [ -n "$val" ] && [ "$val" != '""' ]; then
            pass "$var configurado"
        else
            warn "$var vacio" "Recomendado para produccion. Sin esto no tendras alertas Telegram (errores + capacity monitor) ni watcher."
        fi
    done

    # Verificar passwords no son los defaults
    DB_ROOT_PW=$(grep "^DB_ROOT_PASSWORD=" .env 2>/dev/null | cut -d'=' -f2- | xargs 2>/dev/null || echo "")
    DB_PW=$(grep "^DB_PASSWORD=" .env 2>/dev/null | cut -d'=' -f2- | xargs 2>/dev/null || echo "")

    if [ "$DB_ROOT_PW" = "secret" ]; then
        warn "DB_ROOT_PASSWORD es 'secret' (default)" "Cambiar a un password seguro: openssl rand -base64 24"
    else
        pass "DB_ROOT_PASSWORD no es default"
    fi

    if [ "$DB_PW" = "botpass" ]; then
        warn "DB_PASSWORD es 'botpass' (default)" "Cambiar a un password seguro: openssl rand -base64 24"
    else
        pass "DB_PASSWORD no es default"
    fi
fi

# ==========================================================================
# 4. Puertos disponibles
# ==========================================================================
header "4. Puertos"

# Leer puertos de .env o usar defaults
BOT_PORT=$(grep "^PORT=" .env 2>/dev/null | cut -d'=' -f2- | xargs 2>/dev/null || echo "8080")
DB_PORT=$(grep "^DB_PORT=" .env 2>/dev/null | cut -d'=' -f2- | xargs 2>/dev/null || echo "13308")
NGROK_PORT="14041"

[ -z "$BOT_PORT" ] && BOT_PORT="8080"
[ -z "$DB_PORT" ] && DB_PORT="13308"

check_port() {
    local port=$1
    local service=$2
    local env_var=$3

    # Intentar con ss, luego con netstat, luego con lsof
    local in_use=false
    local used_by=""

    if command -v ss &>/dev/null; then
        used_by=$(ss -tlnp 2>/dev/null | grep ":${port} " | head -1 || true)
    elif command -v netstat &>/dev/null; then
        used_by=$(netstat -tlnp 2>/dev/null | grep ":${port} " | head -1 || true)
    fi

    # Verificar si es nuestro propio container (ya corriendo)
    if [ -n "$used_by" ]; then
        if echo "$used_by" | grep -q "neuro_bot\|docker-proxy"; then
            pass "Puerto $port ($service) - en uso por nuestro container (OK si ya esta corriendo)"
        else
            # Buscar puertos libres cercanos para sugerir
            local suggestions=""
            for try_port in $(seq $((port + 1)) $((port + 10))); do
                if ! ss -tlnp 2>/dev/null | grep -q ":${try_port} " && \
                   ! netstat -tlnp 2>/dev/null | grep -q ":${try_port} "; then
                    suggestions="${suggestions}${try_port}, "
                    [ "$(echo "$suggestions" | tr -cd ',' | wc -c)" -ge 3 ] && break
                fi
            done
            suggestions="${suggestions%, }"
            fail "Puerto $port ($service) OCUPADO" "Cambiar en .env: ${env_var}=<otro_puerto>  |  Libres: ${suggestions:-ninguno cercano}  |  En uso por: $used_by"
        fi
    else
        pass "Puerto $port ($service) disponible"
    fi
}

check_port "$BOT_PORT" "bot HTTP" "PORT"
check_port "$DB_PORT" "MySQL local" "DB_PORT"
check_port "$NGROK_PORT" "ngrok dashboard" "(editar docker-compose.yml ports de ngrok)"

# ==========================================================================
# 5. BD Externa accesible
# ==========================================================================
header "5. BD Externa (SIESA / ZeusSalud_Neuro)"

if [ -f ".env" ]; then
    EXT_HOST=$(grep "^EXTERNAL_DB_HOST=" .env 2>/dev/null | cut -d'=' -f2- | xargs 2>/dev/null || echo "host.docker.internal")
    EXT_PORT=$(grep "^EXTERNAL_DB_PORT=" .env 2>/dev/null | cut -d'=' -f2- | xargs 2>/dev/null || echo "1433")
    EXT_USER=$(grep "^EXTERNAL_DB_USER=" .env 2>/dev/null | cut -d'=' -f2- | xargs 2>/dev/null || echo "")
    EXT_PASS=$(grep "^EXTERNAL_DB_PASSWORD=" .env 2>/dev/null | cut -d'=' -f2- | xargs 2>/dev/null || echo "")
    EXT_DB=$(grep "^EXTERNAL_DB_DATABASE=" .env 2>/dev/null | cut -d'=' -f2- | xargs 2>/dev/null || echo "ZeusSalud_Neuro")

    [ -z "$EXT_HOST" ] && EXT_HOST="host.docker.internal"
    [ -z "$EXT_PORT" ] && EXT_PORT="1433"
    [ -z "$EXT_DB" ] && EXT_DB="ZeusSalud_Neuro"

    # Resolver host.docker.internal a localhost para prueba desde el host
    TEST_HOST="$EXT_HOST"
    if [ "$EXT_HOST" = "host.docker.internal" ]; then
        TEST_HOST="127.0.0.1"
    fi

    # Probar con mysql client si esta disponible
    if command -v mysql &>/dev/null && [ -n "$EXT_USER" ]; then
        if mysql -h "$TEST_HOST" -P "$EXT_PORT" -u "$EXT_USER" -p"$EXT_PASS" -e "SELECT 1" "$EXT_DB" &>/dev/null 2>&1; then
            pass "BD externa accesible ($EXT_HOST:$EXT_PORT/$EXT_DB)"

            # Verificar tablas clave
            TABLE_COUNT=$(mysql -h "$TEST_HOST" -P "$EXT_PORT" -u "$EXT_USER" -p"$EXT_PASS" -N -e "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='$EXT_DB'" 2>/dev/null || echo "0")
            if [ "$TABLE_COUNT" -gt 0 ] 2>/dev/null; then
                pass "BD externa tiene $TABLE_COUNT tablas"
            else
                warn "No se pudieron contar tablas en BD externa" "Verificar permisos del usuario $EXT_USER"
            fi
        else
            fail "No se puede conectar a BD externa ($TEST_HOST:$EXT_PORT/$EXT_DB)" \
                 "Verificar: 1) MySQL corriendo en host  2) Usuario/password correctos  3) bind-address=0.0.0.0 en my.cnf  4) GRANT para $EXT_USER"
        fi
    else
        if [ -z "$EXT_USER" ]; then
            fail "EXTERNAL_DB_USER no configurado" "Editar .env y configurar EXTERNAL_DB_USER y EXTERNAL_DB_PASSWORD"
        else
            warn "mysql client no disponible en el host" "No se puede verificar BD externa desde aqui. Se verificara cuando el container arranque."
        fi
    fi

    # Verificar que el puerto externo de MySQL esta escuchando
    if command -v ss &>/dev/null; then
        if ss -tln 2>/dev/null | grep -q ":${EXT_PORT} "; then
            pass "Puerto $EXT_PORT escuchando en el host (MySQL del centro medico)"
        else
            if [ "$EXT_HOST" = "host.docker.internal" ] || [ "$EXT_HOST" = "127.0.0.1" ] || [ "$EXT_HOST" = "localhost" ]; then
                fail "Puerto $EXT_PORT NO escuchando en el host" "Verificar que MySQL del centro medico esta corriendo: sudo systemctl status mysql"
            fi
        fi
    fi
fi

# ==========================================================================
# 6. Disco y recursos
# ==========================================================================
header "6. Recursos del Sistema"

# Espacio en disco
if command -v df &>/dev/null; then
    # En MSYS/Windows df reporta diferente, buscar la columna que termina en %
    DOCKER_DIR="/var/lib/docker"
    [ -d "$DOCKER_DIR" ] || DOCKER_DIR="/"

    # Extraer porcentaje: buscar campo que contenga % en la salida de df
    DISK_USED=$(df "$DOCKER_DIR" 2>/dev/null | tail -1 | grep -oP '\d+%' | head -1 | tr -d '%' || echo "")
    DISK_AVAIL=$(df -h "$DOCKER_DIR" 2>/dev/null | tail -1 | awk '{for(i=1;i<=NF;i++) if($i ~ /[0-9]+[KMGT]/) last=$i; print last}')

    # Fallback: si no encontro porcentaje, calcular desde bloques usados/total
    if [ -z "$DISK_USED" ] || [ "$DISK_USED" -gt 100 ] 2>/dev/null; then
        TOTAL_BLOCKS=$(df "$DOCKER_DIR" 2>/dev/null | tail -1 | awk '{print $2}')
        USED_BLOCKS=$(df "$DOCKER_DIR" 2>/dev/null | tail -1 | awk '{print $3}')
        if [ -n "$TOTAL_BLOCKS" ] && [ "$TOTAL_BLOCKS" -gt 0 ] 2>/dev/null; then
            DISK_USED=$((USED_BLOCKS * 100 / TOTAL_BLOCKS))
        else
            DISK_USED=""
        fi
    fi

    # Fallback para espacio disponible: leer de df -h columna que tenga G/M/T
    if [ -z "$DISK_AVAIL" ]; then
        DISK_AVAIL=$(df -h "$DOCKER_DIR" 2>/dev/null | tail -1 | awk '{print $4}')
    fi

    if [ -n "$DISK_USED" ] && [ "$DISK_USED" -lt 80 ] 2>/dev/null; then
        pass "Disco: ${DISK_USED}% usado, ${DISK_AVAIL} disponible"
    elif [ -n "$DISK_USED" ] && [ "$DISK_USED" -lt 90 ] 2>/dev/null; then
        warn "Disco: ${DISK_USED}% usado, ${DISK_AVAIL} disponible" "Limpiar espacio: docker image prune -a && docker builder prune"
    elif [ -n "$DISK_USED" ] 2>/dev/null; then
        fail "Disco: ${DISK_USED}% usado, ${DISK_AVAIL} disponible" "Liberar espacio urgente: docker image prune -a && docker builder prune"
    else
        warn "No se pudo determinar espacio en disco" "Verificar manualmente: df -h"
    fi
fi

# RAM
if command -v free &>/dev/null; then
    TOTAL_MB=$(free -m 2>/dev/null | awk '/Mem:/{print $2}')
    AVAIL_MB=$(free -m 2>/dev/null | awk '/Mem:/{print $7}')

    if [ -n "$TOTAL_MB" ] && [ "$TOTAL_MB" -gt 0 ] 2>/dev/null; then
        # Bot necesita: bot(128M) + db(256M) + ngrok(64M) + watcher(32M) = 480M minimo
        MIN_RAM=480
        # high-load: bot(512M) + db(1024M) + ngrok(64M) + watcher(32M) = 1632M
            MIN_RAM_HL=1632
            if [ -n "$AVAIL_MB" ] && [ "$AVAIL_MB" -gt "$MIN_RAM_HL" ] 2>/dev/null; then
                pass "RAM: ${AVAIL_MB}MB disponible de ${TOTAL_MB}MB total (normal: ${MIN_RAM}MB, high-load: ${MIN_RAM_HL}MB)"
            elif [ -n "$AVAIL_MB" ] && [ "$AVAIL_MB" -gt "$MIN_RAM" ] 2>/dev/null; then
                warn "RAM: ${AVAIL_MB}MB disponible de ${TOTAL_MB}MB total" "Suficiente para perfil normal (${MIN_RAM}MB) pero NO para high-load (${MIN_RAM_HL}MB)."
            elif [ -n "$AVAIL_MB" ] 2>/dev/null; then
                warn "RAM: ${AVAIL_MB}MB disponible de ${TOTAL_MB}MB total" "El bot necesita ~${MIN_RAM}MB. Cerrar otros procesos o aumentar RAM."
            fi
    fi
fi

# ==========================================================================
# 7. Docker Socket (para watcher)
# ==========================================================================
header "7. Docker Socket"

if [ -S "/var/run/docker.sock" ]; then
    pass "/var/run/docker.sock existe"
    if [ -r "/var/run/docker.sock" ]; then
        pass "Docker socket legible (watcher podra monitorear)"
    else
        warn "Docker socket no legible por el usuario actual" "Puede funcionar dentro del container. Si el watcher falla: sudo chmod 666 /var/run/docker.sock"
    fi
else
    # En Windows/Mac con Docker Desktop el socket esta en otro lado
    if command -v docker &>/dev/null && docker info &>/dev/null 2>&1; then
        pass "Docker accesible (socket en ubicacion no-estandar, Docker Desktop)"
    else
        warn "Docker socket no encontrado en /var/run/docker.sock" "En Linux: verificar que Docker esta corriendo. En Windows/Mac: Docker Desktop lo maneja automaticamente."
    fi
fi

# ==========================================================================
# 8. Timezone
# ==========================================================================
header "8. Timezone"

if command -v timedatectl &>/dev/null; then
    HOST_TZ=$(timedatectl 2>/dev/null | grep "Time zone" | awk '{print $3}')
    if [ "$HOST_TZ" = "America/Bogota" ]; then
        pass "Timezone del host: America/Bogota"
    else
        warn "Timezone del host: ${HOST_TZ:-desconocido}" "Cambiar con: sudo timedatectl set-timezone America/Bogota  (containers usan TZ propio pero el host afecta logs y cron)"
    fi
else
    CURRENT_TZ=$(date +%Z 2>/dev/null || echo "?")
    if [ "$CURRENT_TZ" = "-05" ] || [ "$CURRENT_TZ" = "COT" ]; then
        pass "Timezone parece Colombia ($CURRENT_TZ)"
    else
        warn "Timezone del host: $CURRENT_TZ" "Los containers tienen TZ=America/Bogota pero el cron del backup usara la hora del host."
    fi
fi

# ==========================================================================
# 9. Volume de datos existente
# ==========================================================================
header "9. Volumes Docker"

if command -v docker &>/dev/null && docker info &>/dev/null 2>&1; then
    # Buscar el volume con nombre que contenga botdbdata
    VOL_NAME=$(docker volume ls --format '{{.Name}}' 2>/dev/null | grep "botdbdata" | head -1 || true)
    if [ -n "$VOL_NAME" ]; then
        VOL_CREATED=$(docker volume inspect "$VOL_NAME" --format='{{.CreatedAt}}' 2>/dev/null | cut -d'T' -f1 || true)
        warn "Volume $VOL_NAME ya existe (creado: $VOL_CREATED)" "Contiene datos de BD anterior. Si es primera vez, no pasa nada. Si quieres empezar limpio: docker volume rm $VOL_NAME (BORRA DATOS)"
    else
        pass "No hay volume botdbdata previo (instalacion limpia)"
    fi
fi

# ==========================================================================
# 10. Ngrok
# ==========================================================================
header "10. Ngrok"

if [ -f ".env" ]; then
    NGROK_TOKEN=$(grep "^NGROK_AUTHTOKEN=" .env 2>/dev/null | cut -d'=' -f2- | xargs 2>/dev/null || echo "")
    NGROK_HOST=$(grep "^NGROK_HOSTNAME=" .env 2>/dev/null | cut -d'=' -f2- | xargs 2>/dev/null || echo "")

    if [ -n "$NGROK_TOKEN" ]; then
        pass "NGROK_AUTHTOKEN configurado"
    else
        fail "NGROK_AUTHTOKEN vacio" "Obtener en https://dashboard.ngrok.com/tunnels/authtokens"
    fi

    if [ -n "$NGROK_HOST" ]; then
        pass "NGROK_HOSTNAME: $NGROK_HOST"

        # Verificar DNS del hostname
        if command -v nslookup &>/dev/null; then
            if nslookup "$NGROK_HOST" &>/dev/null 2>&1; then
                pass "DNS de $NGROK_HOST resuelve correctamente"
            else
                warn "DNS de $NGROK_HOST no resuelve" "Puede ser normal si ngrok lo maneja. Verificar en ngrok dashboard que el dominio esta configurado."
            fi
        fi
    else
        warn "NGROK_HOSTNAME no configurado" "Se usara un subdominio aleatorio de ngrok. Para produccion, configurar un hostname fijo."
    fi
fi

# ==========================================================================
# 11. Watcher (Dockerfile)
# ==========================================================================
header "11. Health Watcher"

if [ -d "watcher" ] && [ -f "watcher/Dockerfile" ]; then
    pass "Directorio watcher/ con Dockerfile"
else
    fail "watcher/Dockerfile no encontrado" "El servicio watcher necesita su propio Dockerfile en watcher/"
fi

# ==========================================================================
# Resumen
# ==========================================================================
echo ""
echo -e "${BOLD}========================================${NC}"
echo -e "${BOLD}         RESUMEN DE VALIDACION          ${NC}"
echo -e "${BOLD}========================================${NC}"
echo -e "  ${GREEN}Pasaron:    $PASS${NC}"
echo -e "  ${YELLOW}Advertencias: $WARN${NC}"
echo -e "  ${RED}Fallaron:   $FAIL${NC}"
echo ""

if [ "$FAIL" -eq 0 ] && [ "$WARN" -eq 0 ]; then
    echo -e "${GREEN}${BOLD}TODO LISTO! Puedes construir con:${NC}"
    echo "  docker compose up -d --build"
    echo ""
    echo -e "  Escalar:  ${CYAN}./scripts/scale-up.sh${NC}   (high-load ~1000 chats/h)"
    echo -e "  Reducir:  ${CYAN}./scripts/scale-down.sh${NC}  (perfil normal)"
    echo -e "  Backup:   ${CYAN}./scripts/backup-db.sh${NC}   (backup BD local)"
elif [ "$FAIL" -eq 0 ]; then
    echo -e "${YELLOW}${BOLD}LISTO CON ADVERTENCIAS. Puedes continuar pero revisa los warnings.${NC}"
    echo "  docker compose up -d --build"
else
    echo -e "${RED}${BOLD}HAY $FAIL ERRORES QUE DEBES CORREGIR antes de construir.${NC}"
    echo "  Corrige los errores marcados con [FAIL] y ejecuta este script de nuevo."
fi

echo ""
exit $FAIL
