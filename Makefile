.PHONY: build run stop dev test test-cover test-race test-integration docker-test docker-test-cover docker-test-race docker-test-integration logs db-shell migrate-up migrate-down

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
	MSYS_NO_PATHCONV=1 docker run --rm -v "$$(pwd):/app" -w "/app" golang:1.23-alpine sh -c "go test ./... && echo OK"

docker-test-cover:
	MSYS_NO_PATHCONV=1 docker run --rm -v "$$(pwd):/app" -w "/app" golang:1.23-alpine sh -c "go test -cover ./internal/... && echo OK"

docker-test-race:
	MSYS_NO_PATHCONV=1 docker run --rm -v "$$(pwd):/app" -w "/app" golang:1.23 sh -c "go test -race ./... && echo OK"

# Tests de integracion (build-tag `integration`). Cada uno se SALTA si su DSN no esta seteado:
#   SIESA_DSN       -> repos SIESA (SQL Server). Ej: sqlserver://sa:pass@host:1433?database=ZeusSalud_Neuro&encrypt=disable
#   LOCAL_TEST_DSN  -> repos locales (MySQL, con migraciones aplicadas).
#                      Ej: botuser:botpass@tcp(127.0.0.1:3306)/neuro_bot?parseTime=true
test-integration:
	go test -tags integration -v ./internal/repository/...

docker-test-integration:
	MSYS_NO_PATHCONV=1 docker run --rm -e SIESA_DSN -e LOCAL_TEST_DSN -v "$$(pwd):/app" -w "/app" golang:1.25 sh -c "go test -tags integration -v ./internal/repository/..."

migrate-up:
	docker compose exec bot ./neuro-bot migrate up

migrate-down:
	docker compose exec bot ./neuro-bot migrate down 1

logs:
	docker compose logs -f bot

db-shell:
	docker compose exec db mysql -u botuser -pbotpass neuro_bot

status:
	docker compose ps

stats:
	docker stats neuro_bot neuro_bot_db neuro_bot_ngrok
