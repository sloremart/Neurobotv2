#!/usr/bin/env bash
# dev.sh — verificación rápida en desarrollo, con caché Docker persistente.
#
# Como Go NO está instalado en el host, todo corre en contenedores efímeros. SIN caché cada corrida
# re-descarga módulos y recompila TODO (~30s build, ~40s tests, ~75s lint). Con estos volúmenes:
# build ~6s, tests sin cambios ~3s (Go reusa resultados), lint ~17s. Docker crea los volúmenes solos.
#
# Uso:  ./scripts/dev.sh check | build | test | vet | lint | fmt | cache-clean
#   check  → build + tests (lo normal mientras iteras)
#   fmt    → arregla formato (lo que suele bloquear el commit)
#   lint   → mismo linter del pre-commit (solo problemas NUEVOS vs HEAD)
set -euo pipefail
cd "$(dirname "$0")/.."

GO_IMAGE="golang:1.25-alpine"
LINT_IMAGE="golangci/golangci-lint:v2.11.0"
CACHE_VOLS=(-v neurobot-go-mod:/go/pkg/mod -v neurobot-go-build:/root/.cache/go-build)
LINT_VOLS=("${CACHE_VOLS[@]}" -v neurobot-lint:/root/.cache/golangci-lint)

drun_go()   { MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd):/app" -w /app "${CACHE_VOLS[@]}" "$GO_IMAGE" sh -c "$1"; }
drun_lint() { MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd):/app" -w /app "${LINT_VOLS[@]}" "$LINT_IMAGE" "$@"; }

case "${1:-check}" in
  check) drun_go "go build ./... && go test ./... 2>&1 | grep -Ev '\\(cached\\)$' || true; echo '✓ check'";;
  build) drun_go "go build ./... && echo '✓ build'";;
  test)  drun_go "go test ./... 2>&1 | grep -E '^(ok|FAIL|---)' ";;
  vet)   drun_go "go vet ./... && echo '✓ vet'";;
  lint)  drun_lint golangci-lint run --new-from-rev=HEAD;;
  fmt)   drun_lint golangci-lint fmt ./... && echo "✓ fmt";;
  cache-clean) docker volume rm neurobot-go-mod neurobot-go-build neurobot-lint 2>/dev/null || true;;
  *) echo "uso: ./scripts/dev.sh check|build|test|vet|lint|fmt|cache-clean"; exit 1;;
esac
