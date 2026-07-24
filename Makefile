.PHONY: build run stop dev test test-cover test-race test-integration docker-test docker-test-cover docker-test-race docker-test-integration logs db-shell migrate-status status stats check vet lint fmt cache-clean

# ─── Caché de desarrollo (Docker) ─────────────────────────────────────────────
# Como Go NO está instalado en el host, todo corre en contenedores efímeros. SIN caché cada
# corrida re-descarga los módulos y recompila TODO (~30s por build, ~75s por commit por el lint).
# Estos volúmenes persisten entre contenedores y bajan build a ~6s, tests sin cambios a ~3s
# (Go reusa resultados) y lint a ~17s. Docker los crea solos la primera vez.
#   GOCACHE  → caché de compilación   MODCACHE → módulos descargados   LINTCACHE → golangci-lint
CACHE_VOLS = -v neurobot-go-mod:/go/pkg/mod -v neurobot-go-build:/root/.cache/go-build
LINT_VOLS  = $(CACHE_VOLS) -v neurobot-lint:/root/.cache/golangci-lint
GO_IMAGE   = golang:1.25-alpine
LINT_IMAGE = golangci/golangci-lint:v2.11.0
DRUN       = MSYS_NO_PATHCONV=1 docker run --rm -v "$$(pwd):/app" -w "/app"


build:
	docker compose build bot

run:
	docker compose up -d

stop:
	docker compose down

dev:
	go run ./cmd/server

test:
	go test ./...

test-cover:
	go test -cover ./internal/...

test-race:
	go test -race ./...

docker-test:
	$(DRUN) $(CACHE_VOLS) $(GO_IMAGE) sh -c "go test ./... && echo OK"

docker-test-cover:
	$(DRUN) $(CACHE_VOLS) $(GO_IMAGE) sh -c "go test -cover ./internal/... && echo OK"

docker-test-race:
	MSYS_NO_PATHCONV=1 docker run --rm -v "$$(pwd):/app" -w "/app" golang:1.25 sh -c "go test -race ./... && echo OK"

# Tests de integracion (build-tag `integration`). Cada uno se SALTA si su DSN no esta seteado:
#   SIESA_DSN       -> repos SIESA (SQL Server). Ej: sqlserver://sa:pass@host:1433?database=ZeusSalud_Neuro&encrypt=disable
#   LOCAL_TEST_DSN  -> repos locales (MySQL, con migraciones aplicadas).
#                      Ej: botuser:botpass@tcp(127.0.0.1:3306)/neuro_bot?parseTime=true
test-integration:
	go test -tags integration -v ./internal/repository/...

docker-test-integration:
	MSYS_NO_PATHCONV=1 docker run --rm -e SIESA_DSN -e LOCAL_TEST_DSN -v "$$(pwd):/app" -w "/app" golang:1.25 sh -c "go test -tags integration -v ./internal/repository/..."

# Las migraciones (golang-migrate, dir ./migrations) se aplican AUTOMÁTICAMENTE al arrancar el
# bot (database.RunMigrations en main.go). No hay subcomando `migrate` en el binario; para
# re-aplicarlas basta reiniciar el contenedor: `make run`. Para inspeccionar el estado:
migrate-status:
	docker compose exec db mysql -u botuser -pbotpass neuro_bot -e "SELECT * FROM schema_migrations;"

logs:
	docker compose logs -f bot

db-shell:
	docker compose exec db mysql -u botuser -pbotpass neuro_bot

status:
	docker compose ps

stats:
	docker stats neuro_bot neuro_bot_db neuro_bot_ngrok

# ─── Verificación rápida en desarrollo (con caché) ────────────────────────────

## check: compila + tests (lo que corres mientras iteras). Con caché: ~6s si nada cambió.
check:
	$(DRUN) $(CACHE_VOLS) $(GO_IMAGE) sh -c "go build ./... && go test ./... 2>&1 | grep -Ev '^ok.*\(cached\)' ; echo CHECK_DONE"

## vet: análisis estático rápido del compilador.
vet:
	$(DRUN) $(CACHE_VOLS) $(GO_IMAGE) sh -c "go vet ./... && echo VET_OK"

## lint: mismo linter del pre-commit (solo problemas NUEVOS vs HEAD). ~17s con caché.
lint:
	$(DRUN) $(LINT_VOLS) $(LINT_IMAGE) golangci-lint run --new-from-rev=HEAD

## fmt: aplica gofumpt+goimports a todo el repo (arregla lo que bloquea el commit).
fmt:
	$(DRUN) $(LINT_VOLS) $(LINT_IMAGE) golangci-lint fmt ./...

## cache-clean: borra los volúmenes de caché (si algo queda inconsistente).
cache-clean:
	-docker volume rm neurobot-go-mod neurobot-go-build neurobot-lint
