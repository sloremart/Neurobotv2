# Setup de calidad y seguridad — neuro-bot

Plantillas calibradas para este proyecto (Go 1.23, sin Go local → Docker, Windows/Git Bash, datos de salud).

## Archivos implementados

| Archivo | Qué hace |
|---|---|
| `.golangci.yml` | Linters v2: gosec + linters de SQL (`sqlclosecheck`, `rowserrcheck`), HTTP (`bodyclose`, `noctx`), errores y concurrencia. Formateadores gofumpt + goimports. |
| `lefthook.yml` | Hook de pre-commit: formato + lint de lo nuevo vía Docker, antes de cada commit. |
| `.github/workflows/ci.yml` | CI en GitHub: build, lint+seguridad, tests con `-race`, cobertura y `govulncheck`. Con `concurrency` para cancelar runs viejos. |
| `scripts/claude-format.sh` | Script de formateo por edición (para un hook opcional de Claude Code; ver más abajo). |

> **Hook de Claude Code (`.claude/settings.json`): NO habilitado** por decisión del equipo. Formatear vía Docker en cada edición añade ~5-10 s por edición y depende de `bash` en el PATH (el entorno es PowerShell). El formato se atrapa igual en el **pre-commit (lefthook)** y en la **CI**. Si más adelante se instala Go localmente, conviene un hook nativo (`gofumpt`) en su lugar.

## Orden de activación

1. **CI** — solo súbela; se activa sola en el próximo push/PR. Es la red de seguridad más importante.
2. **golangci-lint** — la usan tanto la CI como lefthook. No requiere acción aparte.
3. **lefthook** (verificación en cada commit):
   ```bash
   npm install -g lefthook   # o el binario oficial; no necesitas Go
   lefthook install          # escribe los hooks en .git/hooks
   ```

## Prueba de que de verdad bloquea

```bash
git checkout -b prueba-hooks
printf 'package x\nfunc  F( ){}\n' > internal/utils/mal_formato.go   # mal formateado a propósito
git add internal/utils/mal_formato.go
git commit -m "test"        # DEBE ser rechazado por format-check
git checkout -- . ; git clean -fd ; git checkout main ; git branch -D prueba-hooks
```

## Notas del entorno

- **Todo lo local corre en Docker** (no hay Go instalado): `golangci/golangci-lint:v2.11.0` (incluye toolchain Go). La CI usa Go nativo del runner (más rápido). **Verificado: la imagen v2.11.0 existe y el config pasa `golangci-lint config verify`.**
- **Windows/Git Bash:** `MSYS_NO_PATHCONV=1` evita que se corrompa la ruta `/app` del volumen. Ya está en todos los comandos.
- **Versión fijada `v2.11.0`** en `.golangci.yml`, `lefthook.yml`, `ci.yml` y `scripts/claude-format.sh`. Si la cambias, cámbiala en los cuatro.
- **Baseline para legacy:** CI y pre-commit usan `only-new-issues` / `--new-from-rev` → fallan solo por problemas **nuevos**. Verificado que hay deuda de formato histórica (p.ej. gofumpt en `internal/utils`); el baseline evita que bloquee desde el día uno.
- **Validado en este repo:** `go build ./...`, `go vet ./...` y `go test ./...` pasan. El test de integración (`patient_create_integration_test.go`) está aislado con `//go:build integration`, así que NO corre en el `go test ./...` de la CI.
