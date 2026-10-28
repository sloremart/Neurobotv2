# Prueba de humo local del puente de Néctar

Receta para ejercitar el puente Néctar de punta a punta SIN Néctar real y SIN WhatsApp real:
el bot local habla con un **stub** (`cmd/nectar-stub`) y se le inyectan avisos firmados con una
**sonda** (`cmd/probe-notice`). Ambas herramientas se NIEGAN a hablar con un host que no sea de
pruebas (`nectar.IsStubHost`: `localhost`, `127.0.0.1`, `::1`, `nectar-stub`, `nectar_stub`): el
incidente del 2026-09-08 fue un bot local apuntando al Néctar real.

> **ADVERTENCIA: `.env` se carga solo.** `config.Load` ejecuta `godotenv.Load()` sobre el `.env`
> del repo como respaldo, AUNQUE pases `--env-file`: toda variable que tu archivo no defina se
> rellena con la de `.env`. En la primera pasada de esta receta eso arrastró claves REALES
> (Telegram, Bird, OpenAI): el bot llegó a loguear «telegram error alerts enabled» y un `ERROR`
> pudo mandar una alerta al Telegram real. Mitigación obligatoria (preparación B y C): montar un
> `/app/.env` VACÍO (`-v …/empty.env:/app/.env:ro`), dar valores ficticios a las variables
> obligatorias (`BIRD_WORKSPACE_ID`, `BIRD_CHANNEL_ID`, `BIRD_TEAM_FALLBACK`, `OPENAI_API_KEY`)
> y apuntar `BIRD_API_URL` a un host muerto (`http://127.0.0.1:1`) para que ningún envío pueda salir.

Todos los secretos de esta receta valen `stub`. No uses ni copies secretos reales.

## Qué NO hace falta

- **SIESA.** Sin SIESA alcanzable el bot arranca en modo degradado (`/health` →
  `external_db: error`, `status: degraded`; ver `cmd/server/main.go`) y aun así sirve el saludo,
  el menú y la escalación. Los pasos de agendamiento sí lo necesitan; esta receta no los toca.
- Docker nuevo ni imágenes nuevas: Go corre dentro de `golang:1.26-alpine` con los volúmenes de caché.

## Puertos (configurables)

| Pieza | Puerto por defecto de la receta | Nota |
|---|---|---|
| Bot | `8091` | `8090` suele estar ocupado por otro bot en la misma máquina; cámbialo en `PORT` y en `-url` |
| Stub | `9999` | `-addr` del stub y `NECTAR_BASE_URL` del bot |

## Preparación A (paso 1): BD local del bot

```bash
docker compose up -d --no-build db        # neuro_bot_db (MySQL del bot), puerto 13308
```

## Preparación B (paso 1): variables del bot (archivo aparte, sin secretos reales)

`.env` NO sirve tal cual: `godotenv` lo carga como respaldo y metería las llaves reales de Bird,
OpenAI y Telegram (los `ERROR` del log mandarían alertas al Telegram real). Se usa un archivo
propio y se **enmascara** `/app/.env` con uno vacío. El mínimo:

```
PORT=8091
TZ=America/Bogota
DB_HOST=db  DB_PORT=3306  DB_DATABASE=neuro_bot  DB_USER=…  DB_PASSWORD=…  DB_ROOT_PASSWORD=…   # los de la BD local
INTERNAL_API_KEY=…  CENTER_KEY=…  CENTER_NAME=…  BOT_NAME=…                                       # los que ya uses en local
BOT_ENABLED=true
TESTING_ALWAYS_OPEN=true
WHATSAPP_NOTIFICATIONS_ENABLED=false
IVR_NOTIFICATIONS_ENABLED=false
AI_RECOVERY_ENABLED=false
# Bird apagado: ninguna llamada puede salir
BIRD_API_URL=http://127.0.0.1:1
BIRD_API_KEY_WA=stub  BIRD_WEBHOOK_SECRET=stub  BIRD_WORKSPACE_ID=stub  BIRD_CHANNEL_ID=stub
BIRD_TEAM_FALLBACK=stub  OPENAI_API_KEY=stub
# SIESA inalcanzable a propósito
EXTERNAL_DB_DRIVER=siesa  EXTERNAL_DB_HOST=127.0.0.1  EXTERNAL_DB_PORT=1
EXTERNAL_DB_DATABASE=none  EXTERNAL_DB_USER=none  EXTERNAL_DB_PASSWORD=none
# Néctar = SOLO el stub
NECTAR_BASE_URL=http://nectar-stub:9999
NECTAR_SERVICE_KEY=stub
NECTAR_REQUEST_SECRET=stub
NECTAR_NOTICE_SECRET=stub
PROTOCOL_ESCALADO_VERSION=1
```

(Una línea por variable en el archivo real; aquí van juntas para no alargar la receta.)

## Preparación C (pasos 1 y 2): stub y bot (contenedores, sin imágenes nuevas)

```bash
gocmd='-v "$(pwd):/app" -w /app -v neurobot-go-mod:/go/pkg/mod -v neurobot-go-build:/root/.cache/go-build golang:1.26-alpine'

# stub (alias de red «nectar-stub»: es un host de pruebas para IsStubHost)
MSYS_NO_PATHCONV=1 docker run -d --name nectar-smoke-stub --network neurobotv2_botnet \
  --network-alias nectar-stub -p 9999:9999 $gocmd \
  go run ./cmd/nectar-stub -addr :9999 -service-key stub -request-secret stub -scheme http

# bot (con /app/.env enmascarado)
: > /ruta/al/scratch/empty.env
MSYS_NO_PATHCONV=1 docker run -d --name nectar-smoke-bot --network neurobotv2_botnet -p 8091:8091 \
  --env-file /ruta/al/scratch/smoke.env -v /ruta/al/scratch/empty.env:/app/.env:ro $gocmd \
  go run ./cmd/server
```

Comprobar: `curl localhost:8091/health` → `nectar_host: "nectar-stub"`, `external_db: "error"`
(esperado).

## Pasos 3 a 8 (los pasos 1 y 2 son la preparación de arriba)

Sonda: `go run ./cmd/probe-notice -url <url> [-secret stub] [-kind K] [-conversation N] [-text T]
[-event-id E] [-retorno JSON] [-method GET] [-unsigned]` (en contenedor con `--network host`, que funcionó en Docker Desktop for Windows en la corrida de esta receta; si en tu máquina no, usa `host.docker.internal` en `-url`).
El estado de sesión se lee de la BD local (`sessions.phone_number = 'nectar:<conversation>'`).
El stub guarda lo que recibe: `GET localhost:9999/records[?route=messages|escalate|…]`,
`DELETE localhost:9999/records` lo vacía.

| # | Orden | Esperado |
|---|---|---|
| 3 | `probe-notice -url http://localhost:8091/api/webhooks/nectar -conversation 1 -text hola` | `200`; sesión `MAIN_MENU`; el stub registra `POST /messages` tipo `list` |
| 4 | idem `-text asesor` | `200`; el stub recibe `escalate` con `protocol:1`, `reason: patient_asked_for_human`, `facts`, `input`; `sessions.current_state = ESCALATED` y `sessions.status = escalated` |
| 5 | `-kind conversacion_devuelta -retorno '{"opcion":"bot_reanudar"}'` | `200`; sesión de vuelta en `MAIN_MENU`; el stub recibe el menú (`list`) |
| 6 | escalar de nuevo (paso 4) y luego `-kind conversacion_resuelta` | `200`; sesión `completed` (ver nota) |
| 7 | `probe-notice -url http://localhost:8091/api/nectar/capabilities -method GET` y con `-unsigned` | `200` con `protocol:1`; sin firma `401` |
| 8 | repetir el paso 3 con el MISMO `-event-id` | `200` otra vez, log «aviso duplicado», NO hay segundo `POST /messages` |

Notas:

- **Paso 6.** `conversacion_resuelta` solo cierra sesiones **escaladas**; sobre una sesión activa
  el bot lo deja en `agent command but no escalated session` y no hace nada (por diseño). Por eso
  el paso 6 re-escala antes.
- **URL firmada.** El bot reconstruye la URL con `X-Forwarded-Proto` (por defecto `https`, como
  detrás del túnel). La sonda lo manda en `http` cuando el destino es `http://`; sin eso, el
  bot local contesta `401 invalid signature`.
- `GET /api/internal/sessions?phone=nectar:1` (cabecera `X-API-Key`) devuelve la sesión de
  Néctar: las claves `nectar:` se usan tal cual, sin normalizar a E.164.

## Limpieza

```bash
docker rm -f nectar-smoke-bot nectar-smoke-stub
docker compose stop db        # solo si la levantaste tú
```
