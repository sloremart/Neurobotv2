# Monitoreo externo del bot

El `watcher` del compose (`watcher/healthwatch.sh`) solo escucha los eventos del demonio Docker del
servidor: contenedor caído, reiniciado, OOM o `unhealthy`. **No ve** lo que pasa fuera del servidor: si
cae el host, la red, el túnel de ngrok o el DNS, el watcher cae con ellos o no se entera, y nadie avisa.
Para eso hace falta un monitor **externo** que pida `/health` desde internet. Este documento dice cómo
configurarlo; no reemplaza al watcher, lo complementa.

## Qué responde `/health`

`GET https://<NGROK_HOSTNAME>/health` (en prod, el `NGROK_HOSTNAME` del `.env`; por defecto
`app.colibrixa.com`). Es público, sin API key, y no expone secretos (solo estados y el host de Néctar).
Cachea el resultado 5 s, así que pedirlo cada minuto no carga las BD.

| Situación | HTTP | Cuerpo (`status`) | Healthcheck de Docker |
|---|---|---|---|
| Todo bien | 200 | `"status":"ok"` | healthy |
| SIESA caído o sin conexión | 200 | `"status":"degraded"`, `"external_db":"error"` o `"not connected"` | healthy (a propósito) |
| BD local (`neuro_bot`) caída | 503 | `"status":"critical"` | unhealthy → avisa el watcher |
| Bot, host, red o túnel caídos | sin respuesta, timeout o error de ngrok | — | según el caso |

SIESA caído **no** marca al contenedor `unhealthy`: reiniciar el bot no arregla SIESA, y el bot sigue
atendiendo lo que no depende de él. Por eso el estado `degraded` solo lo distingue un monitor que lea
el cuerpo.

## Configuración (UptimeRobot o Better Stack)

Un solo monitor cubre todo:

- **Tipo:** *Keyword* (UptimeRobot) o *Keyword / Expected response* (Better Stack).
- **URL:** `https://<NGROK_HOSTNAME>/health`, método GET.
- **Intervalo:** 1 minuto. Revisar el mínimo del plan: los planes gratuitos suelen quedarse en 3-5 min.
- **Timeout:** 10 s.
- **Condición de alerta:** que la respuesta **no contenga** `"status":"ok"`. Así alertan el 503
  (`critical`), el 200 `degraded` (SIESA), los timeouts, los errores de red y la página de error de
  ngrok cuando el túnel no tiene el hostname.
- **Confirmación:** alertar tras 2 fallos seguidos (evita avisos por un hipo de red de un minuto).
- **Aviso de recuperación:** activado, para saber cuándo volvió sin entrar al servidor.
- **A quién avisa:**
  - Telegram: integración nativa de ambos servicios (se conecta con su bot de Telegram al chat del
    equipo; puede ser el mismo grupo de `TG_CHAT_IDS` del watcher).
  - Celular: la app móvil del servicio (push) y, en Better Stack, llamada o SMS a la persona de turno.

Opcional, si se quiere separar la gravedad: un segundo monitor que alerte solo con HTTP distinto de 200
o sin respuesta (bot o túnel caídos = urgente), y dejar el de keyword para `degraded` (SIESA = aviso).

## Qué hacer cuando alerta

- `degraded` con `external_db: error`: SIESA (192.168.1.207) no responde al bot; revisar el servidor
  SQL y la red hasta él. Ver `docs/TROUBLESHOOTING-PROD.md`.
- `critical`: la BD local no responde; `docker compose ps` y logs de `neuro_bot_db`.
- Sin respuesta: comprobar el host, `docker compose ps`, y el contenedor `neuro_bot_ngrok`
  (`/api/tunnels` en el puerto 14041 del servidor).

## Lo que este monitor no cubre

Un webhook roto con el bot sano (Bird o Néctar dejan de entregar, firma rechazada) deja `/health` en
`ok`. Eso se ve en el aviso URGENTE de `internal/monitor` (con Telegram y puente de Néctar) y en la
auditoría (`docs/GUIA-AUDITORIA.md`), no aquí.

## Revisiones de despliegue (auditoría 2026-11-03)

Pendientes de operación que el código no puede cerrar solo. Ninguno exige tocar el `.env` de golpe:
cada uno dice qué verificar antes.

### Avisos de configuración insegura al arrancar

Al arrancar, el bot escribe un `WARN insecure configuration` por cada uno de estos casos (sin el valor
del secreto, solo el nombre de la variable). No impiden arrancar:

- `DB_PASSWORD` igual a la de `.env.example`, o `DB_ROOT_PASSWORD` vacía o igual a la del ejemplo
  (vacía, el compose pone su valor por defecto, que es el mismo).
- `INTERNAL_API_KEY` de menos de 32 caracteres.
- `EXTERNAL_DB_ENCRYPT` distinto de `true` (o `strict`): el canal a SIESA va en claro.

Tras cada despliegue: `docker compose logs bot --since 5m | grep "insecure configuration"`.

### MySQL local publicado (`DB_EXTERNAL_PORT`, 13308)

El compose publica `13308:3306` en todas las interfaces del servidor. Para cerrarlo:

1. **Antes, ver quién lo usa.** El dashboard (`neuro-dashboard/docker-compose.yml`) entra por
   `DB_HOST=host.docker.internal` y `DB_PORT=13308`, o sea, por el puerto publicado. Ese compose no
   declara `extra_hosts` y en un Linux sin él ese nombre no resuelve, así que el servidor puede tener otra
   configuración: confirmar lo que usa de verdad con `docker exec neuro_dashboard env | grep -E "^DB_(HOST|PORT)="`.
   Ver también si alguien más entra desde la LAN (DBeaver, respaldos, scripts con `13308`):
   `sudo ss -tnp | grep 13308` en horario de uso.
2. **Opción A (recomendada): sacar el dashboard del puerto publicado.** Unir el servicio del dashboard a
   la red del bot (`networks: botnet: {external: true, name: neurobotv2_botnet}` en su compose, con el
   nombre real que da `docker network ls`) y poner `DB_HOST=neuro_bot_db`, `DB_PORT=3306`. Comprobar que
   el dashboard carga datos y, después, quitar el bloque `ports` del servicio `db`.
3. **Opción B: bind restringido.** `"127.0.0.1:${DB_EXTERNAL_PORT:-13308}:3306"` solo sirve a procesos
   del propio host. **Rompe el dashboard en Linux**: `host.docker.internal` llega por la IP del puente de
   Docker, no por 127.0.0.1. Con el dashboard en contenedor, usar la opción A o el bind a la IP del puente
   (`ip -4 addr show docker0`, p. ej. `172.17.0.1:13308:3306`), que no queda expuesta a la LAN.
4. Verificar: `docker compose config | grep -A3 published` muestra el `host_ip` esperado (o no hay
   `ports`); desde otra máquina de la LAN `nc -zv <servidor> 13308` falla; el dashboard sigue cargando.
5. Cambiar las contraseñas débiles es un paso aparte: `DB_PASSWORD` la usan el bot y el dashboard, y
   `DB_ROOT_PASSWORD` solo se aplica al crear el volumen (`botdbdata`); en uno existente hay que cambiarla
   con `ALTER USER` dentro de MySQL y luego en el `.env`.

### Cifrado del canal a SIESA (`EXTERNAL_DB_ENCRYPT`)

Por defecto `disable`: las consultas y resultados (datos clínicos) viajan en claro hasta 192.168.1.207.
Con `true` se cifra todo el canal y se confía en el certificado del servidor (sin validar la CA).

1. **Probar TLS sin tocar el bot**, desde el servidor del bot, con `sqlcmd` (en la versión 18 el
   cifrado es obligatorio por defecto y `-C` confía en el certificado):
   `sqlcmd -S 192.168.1.207,1433 -U <EXTERNAL_DB_USER> -d ZeusSalud_Neuro -C -Q "SELECT encrypt_option FROM sys.dm_exec_connections WHERE session_id = @@SPID"`
   (la contraseña por `SQLCMDPASSWORD`, no en la línea). Debe responder `TRUE`. Si falla con un error de
   TLS o de handshake, el SQL Server no acepta cifrado: no activarlo y hablarlo con el administrador de SIESA.
2. Poner `EXTERNAL_DB_ENCRYPT=true` en el `.env` y `docker compose up -d bot`.
3. Verificar: `/health` con `"external_db":"ok"`, el `WARN` de arriba ya no sale, y en SQL Server
   `SELECT client_net_address, encrypt_option FROM sys.dm_exec_connections WHERE client_net_address = '<IP del servidor del bot>'`
   da `TRUE` en todas las filas.
4. Volver atrás: `EXTERNAL_DB_ENCRYPT=disable` y `docker compose up -d bot`.

### API interna por el túnel (riesgo aceptado)

`/api/internal/*` y `/health/debug` salen a Internet por el túnel de ngrok y por `8080` en todas las
interfaces. Se deja así a propósito: la auditoría (`auditor-neurobot`) y, según su `BOT_API_URL`, el
dashboard entran por el túnel. La única barrera es `INTERNAL_API_KEY` (comparación en tiempo constante y
límite de intentos fallidos por IP), por eso debe ser larga y aleatoria (`openssl rand -hex 32`, ver el
aviso de arriba). Si algún día se cierra: primero mover dashboard y auditor a la red interna o a una
lista de IPs, y luego una política de ngrok que niegue esas rutas; la prueba es que
`curl https://<túnel>/api/internal/logs` dé 403/404 del borde y no 401 del bot.

### ¿Bird firma el DTMF del IVR?

`POST /api/webhooks/voice/dtmf` exige firma. Si Bird no la mandara, cada tecla del IVR recibiría 401 y la
respuesta hablada no saldría (la cita se resuelve igual por la vía `completed`). Para saberlo, en los
días con llamadas del IVR (13:00):

- `voice dtmf fetchCallFlow received` en los logs = la firma pasó: Bird firma. Es la prueba positiva.
- `invalid voice dtmf webhook signature` con `has_signature=false` y ningún `fetchCallFlow received`
  = Bird no firma ese webhook. Los rechazos de firma se muestrean en el log, así que unos pocos bastan.
- `has_signature=true` rechazado = firma con otra clave o URL distinta (revisar
  `BIRD_WEBHOOK_SECRET_VOICE` y la URL pública del túnel).

`docker compose logs bot --since 48h | grep -E "voice dtmf (fetchCallFlow received|webhook signature)"`
o, por la API interna, `/api/internal/logs` filtrando el mismo texto.

### Memoria del contenedor (`GOMEMLIMIT`)

`GOMEMLIMIT=1536MiB` va en el `environment` del servicio `bot`, junto a `memory: 2048M` (~75 %). Se deja
margen porque Ghostscript (OCR de PDF) corre en el mismo cgroup y no cuenta para Go. Si se cambia el
límite, cambiar los dos. Se aplica con `docker compose up -d bot` (no hace falta `--build`); verificar con
`docker compose exec bot env | grep GOMEMLIMIT`.
