# Guía de Contribución — ramas, formateadores, lint, hooks y PRs

> Cómo hacer cambios en Neuro-Bot con la configuración real del repo: ramas desde `main`,
> formateadores (gofumpt + goimports), lint (golangci-lint v2.11.0), hooks de pre-commit (lefthook)
> y Pull Requests a GitHub (`sloremart/Neurobotv2`).
>
> **Premisa clave:** Go NO está instalado localmente. **Todo se valida vía Docker** — igual que el
> build/test del proyecto. En Windows/Git Bash, antepone `MSYS_NO_PATHCONV=1` para no corromper la ruta del volumen.

---

## 0. El flujo en una línea

```
git checkout main && git pull            # parte siempre de main al día
git checkout -b <tipo>/<desc-corta>      # rama nueva
# … cambios …
# formatea → valida (build/test/lint) vía Docker → commit (el hook revalida) → push → PR → merge
```

`main` es la rama por defecto y protegida por la CI. **Nunca se trabaja directo sobre `main`**: siempre rama + PR.

---

## 1. Setup (una sola vez)

1. **Docker Desktop** corriendo (toda la validación corre en contenedores).
2. **Hooks de pre-commit** (lefthook):
   ```bash
   npm install -g lefthook   # o el binario oficial de lefthook
   lefthook install          # instala el hook en .git/hooks
   ```
   Sin esto, el commit no corre las verificaciones locales (pero la CI igual las corre en el PR).
3. **GitHub CLI (opcional, para abrir PRs desde la terminal):** `gh` no viene instalado.
   Instálalo (`winget install GitHub.cli` o `scoop install gh`) y `gh auth login`. Si no, los PRs se abren por la web (ver §6).

> **Pin de versiones:** `golang:1.25` (build/test) y `golangci/golangci-lint:v2.11.0` (lint/formato).
> La misma v2.11.0 está fijada en `lefthook.yml`, `.github/workflows/ci.yml` y `.golangci.yml`.
> Si actualizas una, actualiza las tres.

---

## 2. Crear la rama desde `main`

```bash
git checkout main
git pull origin main          # parte del último main
git checkout -b fix/cierre-escalada-silencio-paciente
```

**Convención de nombres** `<tipo>/<descripcion-en-kebab>` con los tipos de Conventional Commits:

| tipo | cuándo |
|------|--------|
| `feat/` | nueva funcionalidad |
| `fix/` | corrección de bug |
| `test/` | solo tests |
| `docs/` | solo documentación |
| `refactor/` | reestructura sin cambiar comportamiento |
| `chore/` | mantenimiento (deps, config) |
| `perf/` | rendimiento |

---

## 3. Formateadores (gofumpt + goimports)

El proyecto usa **dos formateadores**, declarados en `.golangci.yml` → `formatters`:
- **`gofumpt`** — formato estricto (superset de `gofmt`: alineaciones, espaciado, etc.).
- **`goimports`** — ordena y limpia imports, y **agrupa los del propio módulo aparte**
  (`local-prefixes: github.com/neuro-bot/neuro-bot`): primero stdlib, luego externos, luego internos.

**Aplicar formato a lo que tocaste:**
```bash
# Sobre un directorio (recomendado — el modo por-archivo falla en el contenedor por la ruta):
MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd):/app" -w /app \
  golangci/golangci-lint:v2.11.0 golangci-lint fmt ./internal/<paquete>/

# Ver qué cambiaría sin tocar nada (lo mismo que valida el hook):
MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd):/app" -w /app \
  golangci/golangci-lint:v2.11.0 golangci-lint fmt --diff
```

> El hook **`format-check`** verifica que **TODO el repo** cumpla gofumpt+goimports (la deuda histórica ya
> se limpió). Si `fmt --diff` no imprime nada, estás OK.
>
> ⚠️ **Gotcha frecuente:** al **agregar un campo a un struct** más largo que el más largo existente,
> gofumpt **realinea todo el bloque**. Dos salidas: (a) corre `golangci-lint fmt` sobre ese directorio y
> aceptas el realineado, o (b) nombras el campo ≤ al más largo actual para no disparar la realineación.

---

## 4. Validar localmente (antes de commitear)

Aunque el hook corre formato+lint, **los tests NO están en el hook** (van en la CI). Conviene correrlos tú:

```bash
# 1) Build de todo
MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd):/app" -w /app golang:1.25 go build ./...

# 2) Tests (idealmente con -race, como la CI). Acota a los paquetes que tocaste para ir rápido:
MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd):/app" -w /app golang:1.25 \
  go test -race ./internal/<paquete>/...

# 3) Lint incremental (idéntico al hook): solo problemas NUEVOS vs el último commit
MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd):/app" -w /app \
  golangci/golangci-lint:v2.11.0 golangci-lint run --new-from-rev=HEAD
```

Atajos equivalentes en el `Makefile`: `make docker-test`, `make docker-test-race`,
`make docker-test-integration` (estos últimos requieren `SIESA_DSN`/`LOCAL_TEST_DSN`; ver
`docs/test-coverage-baseline`… y el propio Makefile).

### Linters activos (qué te puede frenar)
`standard` (errcheck, govet, ineffassign, staticcheck, unused) **+** `gosec` (seguridad — datos de salud),
`rowserrcheck`/`sqlclosecheck` (SQL con `database/sql`), `bodyclose`/`noctx` (HTTP), `errorlint`/`nilerr`,
`contextcheck`/`copyloopvar` (concurrencia), `gocritic`, `revive`, `unparam`.
En archivos `_test.go` se relajan `gosec`/`bodyclose`/`noctx`/`unparam`.

---

## 5. Commit (el hook revalida)

Mensajes en **Conventional Commits**: `<tipo>(<área>): <resumen en imperativo>` + cuerpo opcional.

```bash
git add -A
git commit -m "fix(escalacion): cerrar chat escalado solo por silencio del paciente"
```

Al commitear, lefthook corre **en paralelo no** (`parallel: false`), en orden:
1. **`format-check`** — `golangci-lint fmt --diff` sobre todo el repo (falla si algo no está formateado).
2. **`lint-new`** — `golangci-lint run --new-from-rev=HEAD` (falla solo por problemas **nuevos**).

Tarda ~60s (compila dentro del contenedor de lint). Si falla, **arregla y reintenta** — no uses `--no-verify`.

> Los commits del repo cierran con un footer `Co-Authored-By:` cuando aplica. Mantén el estilo del historial.

### Errores típicos del hook y cómo resolverlos
| Lint | Causa | Fix |
|------|-------|-----|
| `gofumpt: File is not properly formatted` | formato | `golangci-lint fmt ./dir/` (o alinear a mano con `--diff`) |
| `errcheck: Error return value ... not checked` | `w.Write(...)`, `rows.Close()` | `_, _ = w.Write(...)` · `defer func(){ _ = rows.Close() }()` |
| `revive: unused-parameter 'ctx'` | param no usado | renómbralo a `_` |
| `revive: exported ... should have comment` | método/identificador exportado nuevo | agrega un comentario `// Nombre ...` |
| Aparece un lint **nuevo** tras arreglar otro | `--new-from-rev` reevalúa | itera: arregla, vuelve a correr |

---

## 6. Push y abrir el Pull Request

```bash
git push -u origin fix/cierre-escalada-silencio-paciente
```

**Opción A — GitHub CLI** (si instalaste `gh`):
```bash
gh pr create --base main --fill            # usa el último commit como título/cuerpo
# o interactivo: gh pr create --base main
```

**Opción B — Web** (sin `gh`): tras el push, GitHub muestra el enlace
`https://github.com/sloremart/Neurobotv2/pull/new/<tu-rama>` en la salida del push, o entra al repo y
pulsa **“Compare & pull request”**. Base: `main`.

**El PR debe describir:** qué cambia y por qué, cómo se validó (build/test/lint) y, si aplica, riesgos o
pasos manuales (migraciones, variables nuevas de entorno). Si tocaste algo de comportamiento, enlaza el flujo afectado.

---

## 7. La CI (corre sola en el PR)

`.github/workflows/ci.yml` se dispara en **cada PR** y en **push a `main`**. Job `quality` (Ubuntu, Go 1.25):

1. **Build** — `go build ./...`.
2. **Lint + formato + seguridad** — golangci-lint v2.11.0. En **PR** usa `only-new-issues: true` (solo lo nuevo); en **push a main** corre completo. Incluye gosec y staticcheck.
3. **Tests + race + cobertura** — `go test ./... -race -coverprofile=coverage.out -covermode=atomic`.
4. **Vulnerabilidades** — `govulncheck ./...` (deps con CVEs conocidos).

> Nota: la CI **no** corre los tests `integration` (build-tag), porque necesitan BD reales (`SIESA_DSN`/`LOCAL_TEST_DSN`).
> Esos se corren a mano con `make docker-test-integration` (ver el `Makefile`).

El PR no se puede mergear hasta que la CI pase (✓ verde).

---

## 8. Merge y limpieza

Tras aprobación + CI verde, mergea a `main` (preferentemente **squash** o **fast-forward** para historia limpia).
Desde la web: botón **Merge**. Desde local (fast-forward, si nadie más tocó main):
```bash
git checkout main && git pull origin main
git merge --ff-only fix/cierre-escalada-silencio-paciente
git push origin main
git branch -d fix/cierre-escalada-silencio-paciente   # borra la rama local ya mergeada
```

---

## 9. Cheatsheet

```bash
# Rama desde main
git checkout main && git pull origin main && git checkout -b feat/mi-cambio

# Formato (aplicar)            sobre el directorio que tocaste
MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd):/app" -w /app golangci/golangci-lint:v2.11.0 golangci-lint fmt ./internal/<pkg>/
# Formato (verificar, == hook)
MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd):/app" -w /app golangci/golangci-lint:v2.11.0 golangci-lint fmt --diff
# Lint incremental (== hook)
MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd):/app" -w /app golangci/golangci-lint:v2.11.0 golangci-lint run --new-from-rev=HEAD
# Build + tests con race
MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd):/app" -w /app golang:1.25 sh -c "go build ./... && go test -race ./internal/<pkg>/..."

# Commit (corre el hook) · push · PR
git add -A && git commit -m "feat(area): resumen"
git push -u origin feat/mi-cambio
gh pr create --base main --fill        # o por la web

# Merge fast-forward
git checkout main && git pull && git merge --ff-only feat/mi-cambio && git push origin main
```

---

## 10. Reglas de oro

1. **Nunca commitees directo a `main`** — rama + PR siempre.
2. **No uses `--no-verify`** para saltar el hook; si falla, arréglalo (la CI lo volvería a frenar).
3. **Formatea con gofumpt+goimports** antes de commitear; el repo entero debe pasar `fmt --diff`.
4. **Corre los tests `-race`** localmente de lo que tocaste (el hook no los corre, la CI sí).
5. **Pin de versión coherente** (golangci-lint v2.11.0 en los tres archivos; Go 1.25).
6. Si agregas **variable de entorno**, documéntala en `.env.example` y en `docs/env-*.env`.
7. Si agregas **migración**, numérala tras la última y prueba `up`/`down`.
