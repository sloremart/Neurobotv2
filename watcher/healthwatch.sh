#!/usr/bin/env bash
set -euo pipefail

# ===== Configuración =====
TG_BOT_TOKEN="${TG_BOT_TOKEN:-}"
TG_CHAT_IDS="${TG_CHAT_IDS:-}"
# Contenedores a ignorar (separados por coma): ej. "neuro_bot_watcher"
IGNORE_CONTAINERS="${IGNORE_CONTAINERS:-}"
# 0=notificaciones solo unhealthy/stop; 1=todo (incluye healthy/start)
VERBOSE="${VERBOSE:-0}"
# Proyecto de compose a vigilar. VACÍO = todos los contenedores del demonio, que en un servidor con
# varios stacks significa alertar de contenedores ajenos como si fueran el bot.
COMPOSE_PROJECT="${COMPOSE_PROJECT:-}"
# Segundos de silencio por contenedor tras una alerta. Un bucle de reinicio emite un evento cada
# ~13s; sin esto son ~5 mensajes por minuto durante horas y el canal se vuelve inservible justo
# cuando hay que leerlo. Los eventos suprimidos se cuentan y se informan en la siguiente alerta.
COOLDOWN_SECONDS="${COOLDOWN_SECONDS:-300}"

declare -A LAST_ALERT_AT   # contenedor -> epoch de la última alerta enviada
declare -A SUPPRESSED      # contenedor -> eventos silenciados desde entonces
declare -A IN_ALARM        # contenedor -> 1 si está en estado de alarma (para avisar la recuperación)

# ===== Funciones auxiliares =====
contains() {
    case ",$1," in
        *",${2},"*) return 0;;
        *) return 1;;
    esac
}

notify_telegram() {
    local text="$1"
    [ -z "$TG_BOT_TOKEN" ] || [ -z "$TG_CHAT_IDS" ] && return 0

    IFS=',' read -ra CHAT_ARRAY <<< "$TG_CHAT_IDS"

    for chat_id in "${CHAT_ARRAY[@]}"; do
        chat_id=$(echo "$chat_id" | xargs)
        [ -z "$chat_id" ] && continue

        # Texto plano a propósito: sin parse_mode. Los nombres de contenedor llevan guión bajo
        # (neuro_bot_db) y con parse_mode=Markdown Telegram responde 400 y el mensaje SE PIERDE —
        # perder una alerta es peor que verla sin negritas.
        curl -s "https://api.telegram.org/bot${TG_BOT_TOKEN}/sendMessage" \
            -d chat_id="$chat_id" \
            --data-urlencode text="$text" >/dev/null || true
    done
}

notify_all() {
    local msg="$1"
    local timestamp
    timestamp=$(date '+%Y-%m-%d %H:%M:%S')
    echo "[$timestamp] $msg"
    notify_telegram "$msg"
}

# notify_throttled envía una alerta de fallo respetando el cooldown por contenedor.
# Uso: notify_throttled <contenedor> <mensaje>
notify_throttled() {
    local name="$1" msg="$2"
    local now last suppressed
    now=$(date +%s)
    last="${LAST_ALERT_AT[$name]:-0}"
    suppressed="${SUPPRESSED[$name]:-0}"

    IN_ALARM[$name]=1

    if [ $((now - last)) -lt "$COOLDOWN_SECONDS" ]; then
        SUPPRESSED[$name]=$((suppressed + 1))
        echo "[$(date '+%Y-%m-%d %H:%M:%S')] (silenciado, ${SUPPRESSED[$name]} desde la última alerta) $msg"
        return 0
    fi

    if [ "$suppressed" -gt 0 ]; then
        local window
        if [ "$COOLDOWN_SECONDS" -ge 60 ]; then
            window="$((COOLDOWN_SECONDS / 60)) min"
        else
            window="${COOLDOWN_SECONDS}s"
        fi
        msg="$msg
(+${suppressed} eventos similares silenciados en los últimos ${window} — parece un bucle de reinicio)"
    fi

    LAST_ALERT_AT[$name]=$now
    SUPPRESSED[$name]=0
    notify_all "$msg"
}

# notify_recovered avisa que un contenedor volvió, pero SOLO si antes se alertó por él. Sin esto, con
# VERBOSE=0 solo llegan malas noticias: te enterás de la caída y nunca de que se recuperó, así que no
# hay forma de saber si sigue caído sin entrar al servidor.
notify_recovered() {
    local name="$1" msg="$2"
    if [ "${IN_ALARM[$name]:-0}" = "1" ]; then
        IN_ALARM[$name]=0
        LAST_ALERT_AT[$name]=0
        local suppressed="${SUPPRESSED[$name]:-0}"
        SUPPRESSED[$name]=0
        if [ "$suppressed" -gt 0 ]; then
            msg="$msg
(hubo +${suppressed} eventos silenciados durante la incidencia)"
        fi
        notify_all "$msg"
    elif [ "$VERBOSE" = "1" ]; then
        notify_all "$msg"
    fi
}

# ===== Inicio del watcher =====
EVENT_ARGS=(--format '{{json .}}' --filter 'type=container')
SCOPE="todos los contenedores del host"
if [ -n "$COMPOSE_PROJECT" ]; then
    EVENT_ARGS+=(--filter "label=com.docker.compose.project=${COMPOSE_PROJECT}")
    SCOPE="proyecto ${COMPOSE_PROJECT}"
fi

notify_all "🔔 Docker Watcher iniciado — vigilando: ${SCOPE} (cooldown ${COOLDOWN_SECONDS}s)"

# Escuchar eventos de Docker en tiempo real
docker events "${EVENT_ARGS[@]}" | while read -r line; do
    status=$(echo "$line" | jq -r '.status // empty')
    type=$(echo "$line"   | jq -r '.Type // empty')
    name=$(echo "$line"   | jq -r '.Actor.Attributes.name // empty')
    health=$(echo "$line" | jq -r '.Actor.Attributes.health_status // empty')

    # Solo procesar eventos de contenedores
    [ "$type" != "container" ] && continue
    [ -z "$name" ] && continue

    # Ignorar contenedores especificados
    if [ -n "$IGNORE_CONTAINERS" ] && contains "$IGNORE_CONTAINERS" "$name"; then
        continue
    fi

    # Procesar eventos según el tipo
    case "$status" in
        health_status)
            if [ "$health" = "unhealthy" ]; then
                notify_throttled "$name" "🚑 UNHEALTHY: $name — requiere atención"
            elif [ "$health" = "healthy" ]; then
                notify_recovered "$name" "✅ RECUPERADO: $name volvió a estado healthy"
            fi
            ;;
        start)
            # `start` NO es recuperación: en un bucle de reinicio llega inmediatamente después de cada
            # `die`, así que tratarlo como tal alternaría alerta/recuperación y DUPLICARÍA el ruido en
            # vez de reducirlo. La recuperación de verdad es que el healthcheck vuelva a pasar
            # (health_status=healthy), que es justo lo que un contenedor en bucle nunca alcanza.
            [ "$VERBOSE" = "1" ] && notify_all "▶️ ARRANCÓ: $name"
            ;;
        die|stop)
            notify_throttled "$name" "🛑 DETENIDO: $name — servicio caído"
            ;;
        restart)
            notify_throttled "$name" "🔁 REINICIO: $name"
            ;;
        kill)
            notify_throttled "$name" "💀 KILL: $name — proceso terminado forzosamente"
            ;;
        oom)
            notify_throttled "$name" "💥 SIN MEMORIA: $name — OOM"
            ;;
    esac
done

# Si llegamos aquí, el stream de eventos se cortó (reinicio del demonio Docker, socket caído). El
# watcher queda CIEGO y en silencio, que es el peor modo de fallo posible para un vigilante: hay que
# avisarlo y salir distinto de cero para que Docker lo reinicie (restart: unless-stopped).
notify_all "⚠️ Docker Watcher: se cortó el stream de eventos — reiniciando (sin esto quedaría ciego y en silencio)"
exit 1
