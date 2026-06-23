#!/usr/bin/env bash
# scripts/claude-format.sh
# Invocado por el hook PostToolUse de Claude Code tras cada edición.
# Lee el JSON del evento por stdin, extrae el archivo editado y, si es .go,
# lo formatea con golangci-lint vía Docker (Go no está instalado localmente).
#
# Es defensivo: ante cualquier problema sale con 0 para no interrumpir la sesión.

set -uo pipefail

payload="$(cat)"

# Extrae file_path del JSON sin depender de jq (puede no estar instalado).
file="$(printf '%s' "$payload" | grep -o '"file_path"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | sed 's/.*"file_path"[[:space:]]*:[[:space:]]*"//; s/"$//')"

# Solo actuamos sobre archivos Go.
case "$file" in
  *.go) ;;
  *) exit 0 ;;
esac

# Formatea solo ese archivo. Silencioso y tolerante a fallos.
MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd):/app" -w /app \
  golangci/golangci-lint:v2.11.0 golangci-lint fmt "$file" >/dev/null 2>&1 || true

exit 0
