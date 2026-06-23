# === Stage 1: Build ===
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git
WORKDIR /app

# Cache de dependencias
COPY go.mod go.sum ./
RUN go mod download

# Compilar
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o neuro-bot ./cmd/server

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
