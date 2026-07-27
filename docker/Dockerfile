# === Stage 1: Build ===
FROM golang:1.25-alpine AS builder

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
# safe.directory: el .git copiado pertenece a otro usuario dentro del contenedor y git, si no, aborta
# con "dubious ownership".
COPY . .
RUN git config --global --add safe.directory /app && \
    COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)" && \
    BUILT="$(date -u +%Y-%m-%dT%H:%M:%SZ)" && \
    echo "building commit=$COMMIT at $BUILT" && \
    CGO_ENABLED=0 GOOS=linux go build \
      -ldflags="-s -w -X main.buildCommit=$COMMIT -X main.buildTime=$BUILT" \
      -o neuro-bot ./cmd/server

# === Stage 2: Runtime ===
FROM alpine:3.21

RUN apk --no-cache add ca-certificates tzdata wget ghostscript

WORKDIR /app

COPY --from=builder /app/neuro-bot .
COPY --from=builder /app/migrations ./migrations

ENV TZ=America/Bogota
# Tell Go GC to stay under 90% of the container memory limit (1024M → ~920MiB).
# Without this, Go can allocate beyond the cgroup limit before GC runs, causing OOM kills.
ENV GOMEMLIMIT=920MiB

EXPOSE 8080

ENTRYPOINT ["./neuro-bot"]
