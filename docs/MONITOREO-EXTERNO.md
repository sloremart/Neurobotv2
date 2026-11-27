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

Con la línea vieja de Néctar armada (`NECTAR_LEGACY_*`) el cuerpo añade `"nectar_legacy":"armed"`; sin armar (o sin puente) la clave no aparece. No cambia `status` ni el HTTP: sirve para comprobar tras un despliegue que la línea vieja quedó encendida.

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
- `EXTERNAL_DB_ENCRYPT` distinto de `true` (o `strict`, ver abajo): el canal a SIESA va en claro.

Tras cada despliegue: `docker compose logs bot --since 5m | grep "insecure configuration"`.

### Puertos del bot solo en el servidor (desde 2026-11-27)

El compose publica el bot (`PORT`, 8085 en producción), su base (13308) y la API de ngrok (14041) solo en
`127.0.0.1` (`HOST_BIND_IP`): la red de la IPS ya no llega a ellos. Desde el propio servidor siguen sirviendo
`curl localhost:8085/health`, `mysql -h 127.0.0.1 -P 13308` y `curl localhost:14041/api/tunnels`. ngrok y el
dashboard entran por la red interna `neurobotv2_botnet`. El dashboard se une a ella como red externa y usa
`neuro_bot_db:3306` y `http://neuro_bot:${BOT_PORT:-8085}` (rama `feat/migracion-nectar` de neuro-dashboard).

1. **Antes de desplegar, ver si alguien más entra desde la red.** Correr varias veces en horario de uso:
   `ss -tn state established '( sport = :13308 or sport = :8085 )'`. Las IP `172.x` son contenedores; una
   `192.168.x` es un equipo de la red que dejaría de conectar. Quien necesite la base desde su PC entra por
   túnel SSH: `ssh -L 13308:127.0.0.1:13308 root@<servidor>` y apunta su programa a `127.0.0.1:13308`.
2. **Orden:** primero el bot (crea la red con su nombre fijo), después el dashboard (`docker compose up -d
   --build` en su carpeta). Entre los dos, el dashboard viejo pierde la base por un minuto.
3. Verificar: `docker port neuro_bot` y `docker port neuro_bot_db` muestran `127.0.0.1`; el dashboard carga
   datos y su `/api/health` dice `bot_reachable: true`; desde otro equipo, `nc -zv <servidor> 13308` falla.
4. En un PC de desarrollo que necesite entrar desde otra máquina o por `host.docker.internal` (el harness e2e),
   `HOST_BIND_IP=0.0.0.0` en su `.env`.

### MySQL 8.4 (desde 2026-11-27)

La base del bot pasa de `mysql:8.0` a `mysql:8.4.13` (la 8.0 salió de soporte en abril de 2026). La subida es en
el sitio al recrear el contenedor y **no tiene vuelta atrás**: la 8.0 ya no abre los datos convertidos. Por
eso, antes del despliegue, `scripts/backup-db.sh` y comprobar que el `.gz` no está vacío. Volver atrás =
volumen nuevo con 8.0 y restaurar ese respaldo. Los usuarios siguen con `mysql_native_password`, que en 8.4 va
apagado por defecto: el compose lo enciende (`--mysql-native-password=ON`; la opción
`--default-authentication-plugin` ya no existe). Pasar a `caching_sha2_password` es un paso aparte (`ALTER USER`
del bot y root, y recrear bot y dashboard). Probado en local con la copia de producción: subida de datos 8.0.46 →
8.4.13 sin errores, migraciones en 48 y el dashboard con todas sus rutas.

### Contraseñas débiles de la base

Cambiarlas es un paso aparte: `DB_PASSWORD` la usan el bot y el dashboard, y
`DB_ROOT_PASSWORD` solo se aplica al crear el volumen (`botdbdata`); en uno existente hay que cambiarla
con `ALTER USER` dentro de MySQL y luego en el `.env`. Después hay que **recrear el bot y el dashboard**
(`docker compose up -d bot` y el `up -d` del compose del dashboard; un `restart` no relee el `.env`): ambos
cortan unos segundos mientras reinician, y entre el `ALTER USER` y la recreación el que siga con la
contraseña vieja falla al conectar. Hacerlo juntos, en un momento de poco uso.

### Cifrado del canal a SIESA (`EXTERNAL_DB_ENCRYPT`)

Por defecto `disable`: las consultas y resultados (datos clínicos) viajan en claro hasta 192.168.1.207.
Con `true` se cifra todo el canal y se confía en el certificado del servidor (sin validar la CA).
`strict` (TDS 8.0) NO sirve con el certificado autofirmado de la LAN: fuerza verificar el certificado del
servidor y, al no poder validarlo, corta la conexión a SIESA (`/health` pasa a `degraded`). Usar `true`
mientras SIESA no tenga un certificado de una CA que el contenedor del bot reconozca.

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
