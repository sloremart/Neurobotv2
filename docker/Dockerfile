# syntax=docker/dockerfile:1.19
# La línea de arriba fija el frontend de BuildKit que entiende `COPY --exclude` sin la variante «labs»
# (verificado: 1.18 lo rechaza, 1.19 lo acepta). BuildKit lo baja de Docker Hub en el primer build.

# === Stage 1: Build ===
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git
WORKDIR /app

# Cache de dependencias
COPY go.mod go.sum ./
RUN go mod download

# Compilar.
#
# El binario se SELLA con el commit y la fecha de build (-ldflags -X), y /health/debug los expone. Sin
# esto no había forma de saber qué código corre en producción y "¿entró el deploy?" se respondía por
# inferencia (auditoría, ciclos 125 y 129). El commit se saca del .git que ya viene en el contexto de
# build, así que NO hay que pasar build-args: `docker compose up -d --build bot` sigue siendo el mismo
# comando de siempre. Si el .git no estuviera disponible, queda "unknown" en vez de fallar el build.
#
# El .git NO se copia a la capa (`--exclude`): se monta solo durante el RUN, que no lo guarda. Así no
# quedan en la caché del servidor `.git/config` (la URL del remoto, que puede llevar un token) ni las
# ramas y reflogs locales.
#
# safe.directory: el .git montado pertenece a otro usuario dentro del contenedor y git, si no, aborta
# con "dubious ownership".
COPY --exclude=.git . .
RUN --mount=type=bind,source=.git,target=/app/.git \
    git config --global --add safe.directory /app && \
    COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)" && \
    BUILT="$(date -u +%Y-%m-%dT%H:%M:%SZ)" && \
    echo "building commit=$COMMIT at $BUILT" && \
    CGO_ENABLED=0 GOOS=linux go build \
      -ldflags="-s -w -X main.buildCommit=$COMMIT -X main.buildTime=$BUILT" \
      -o neuro-bot ./cmd/server

# === Stage 2: Runtime ===
# Versión fija de la base. Ghostscript abre los PDF que mandan los pacientes: se fija la rama 10.08 (`~`
# admite los parches -rN que Alpine publica en esa rama, que son justo los de seguridad). En 3.24 gs está
# en `community`, con soporte solo hasta la siguiente versión estable de Alpine: REVISAR esta fijación (y
# la imagen de bot-logs-init en el compose) en cada versión nueva de Alpine. Una rama sin soporte sigue
# sirviendo su gs viejo sin error; el CI comprueba la versión mínima de gs (GS_MIN_VERSION en ci.yml).
FROM alpine:3.24.3

RUN apk --no-cache add ca-certificates tzdata wget 'ghostscript~10.08'

# El bot corre sin root: un PDF que lograra evadir -dSAFER no ejecuta como root dentro del contenedor.
# UID/GID fijos (10001): el servicio bot-logs-init del compose deja el volumen de logs con ese dueño.
RUN addgroup -S -g 10001 bot && adduser -S -D -u 10001 -G bot -h /home/bot bot

WORKDIR /app

COPY --from=builder /app/neuro-bot .
COPY --from=builder /app/migrations ./migrations
# /app/logs es lo único que el bot escribe en /app (los temporales del OCR van a /tmp). Un volumen nuevo
# copia este dueño; uno que ya existía lo ajusta bot-logs-init.
RUN mkdir -p /app/logs && chown bot:bot /app/logs

USER bot:bot

ENV TZ=America/Bogota
# GOMEMLIMIT no va aquí: depende del límite de memoria del contenedor y se fija junto a él, en el
# `environment` del servicio bot de docker-compose.yml.

EXPOSE 8080

ENTRYPOINT ["./neuro-bot"]
