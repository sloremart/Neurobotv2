# Troubleshooting de Produccion y Backup

Guia para diagnosticar y resolver problemas al desplegar el bot en un servidor de produccion.

---

## Despliegue / Reconstrucción en Producción (desde cero)

Pasos para levantar o reconstruir el proyecto en el servidor. **Las migraciones de la BD local se
aplican AUTOMÁTICAMENTE** al arrancar el bot (golang-migrate corre `./migrations` desde `main.go`):
no hay comando manual de migración, basta con `up`.

```bash
# 1. Clonar el repo (o `git pull` si ya está clonado)
git clone https://github.com/sloremart/Neurobotv2.git
cd Neurobotv2

# 2. Crear el .env. Usar docs/env-production.env como base y completar TODOS los secretos.
cp docs/env-production.env .env
nano .env                       # revisar EXTERNAL_DB_*, BIRD_*, OPENAI_*, PORT=8085, etc.

# 3. (recomendado) Validar antes de construir
./scripts/pre-deploy-check.sh   # corregir los [FAIL]

# 4. Construir y levantar TODO (bot + MySQL local + ngrok + watcher)
docker compose up -d --build
```

Al arrancar, el bot en orden:
1. Crea/conecta la **BD local MySQL** (servicio `db`; se crea sola con las vars `MYSQL_*`).
2. **Aplica todas las migraciones** `001..NNN` sobre `neuro_bot` → crea todas las tablas **+ el seed
   de `center_locations`** (migración 024). Si la BD ya tenía migraciones, solo aplica las nuevas.
3. Conecta a **SIESA** (SQL Server) y arranca los workers.

### Verificar el despliegue

```bash
docker compose ps                                       # todos healthy
docker compose logs bot | grep "migrations applied"     # version = última migración, dirty:false
make migrate-status                                     # o: SELECT * FROM schema_migrations;
curl -s http://localhost:8085/health                    # {"external_db":"ok","local_db":"ok","status":"ok"}
```

### Reconstruir desde cero (¡cuidado con los datos!)

```bash
docker compose down            # detiene; CONSERVA el volumen botdbdata (datos intactos)
docker compose up -d --build   # reconstruye la imagen y vuelve a levantar
```

> ⚠️ `docker compose down -v` **BORRA el volumen** `botdbdata` (sesiones, WAL, scheduler, etc.). Tras un
> `-v`, las migraciones recrean el esquema vacío + el seed, pero **se pierden todos los datos locales**.
> Hacer **backup antes** (ver sección Backup). La BD clínica (SIESA) NO se toca: es externa.

---

## Escenarios de Fallo y Solucion

### 1. Puertos Ocupados

**Sintoma**: `Error starting userland proxy: listen tcp4 0.0.0.0:8080: bind: address already in use`

**Puertos que usa el bot** (host):
| Puerto Host | Servicio | Uso | Variable |
|-------------|----------|-----|----------|
| `8080` (prod usa **8085**) | bot | HTTP server (webhooks + health) | `PORT` |
| `13308` | db | MySQL acceso externo | `DB_EXTERNAL_PORT` |
| `14041` | ngrok | Dashboard ngrok | hardcodeado en compose |

> En el servidor de producción el bot corre en **`PORT=8085`** (el 8080 estaba ocupado). El mapeo del
> compose es `${PORT}:${PORT}`, así que basta cambiar `PORT` en `.env`. El **14041 de ngrok está
> hardcodeado** en `docker-compose.yml` (no sale del `.env`): si está ocupado, editarlo ahí.

**Diagnosticar**:
```bash
# Ver que ocupa el puerto
sudo lsof -i :8080
sudo lsof -i :13308
sudo lsof -i :14041

# O con ss
sudo ss -tlnp | grep -E '8080|13308|14041'
```

**Solucionar**: Cambiar puertos en `.env` sin tocar docker-compose.yml:
```env
PORT=8090              # Cambia el puerto host del bot (mapeo ${PORT}:${PORT})
DB_EXTERNAL_PORT=13309 # Cambia el puerto host del MySQL (mapeo ${DB_EXTERNAL_PORT}:3306)
```
> OJO: NO confundir con `DB_PORT` (=3306), que es el puerto INTERNO con el que el bot se conecta al
> servicio `db`. El que se expone al host es **`DB_EXTERNAL_PORT`**.

Para ngrok (no tiene variable), editar `docker-compose.yml`:
```yaml
ngrok:
  ports:
    - "14042:4040"   # Cambiar 14041 por otro
```

> **Nota**: El puerto interno de MySQL **sí es fijo en 3306** (el mapeo es `${DB_EXTERNAL_PORT}:3306`).
> El del **bot NO es fijo**: el mapeo es `${PORT}:${PORT}` y el binario bindea a `PORT`, así que al
> cambiar `PORT` cambian **ambos lados** (host y container). En prod el bot escucha en 8085.

---

### 2. BD Externa SIESA No Accesible (SQL Server)

> **IMPORTANTE**: La BD clínica externa es **SIESA → `ZeusSalud_Neuro` (SQL Server)**, NO MySQL.
> El antiguo sistema Antares/`datosipsndx` (MySQL) fue eliminado. Conexión por **TDS, puerto 1433**.
> Variables: `EXTERNAL_DB_HOST` (prod: `192.168.1.207`), `EXTERNAL_DB_PORT=1433`,
> `EXTERNAL_DB_DATABASE=ZeusSalud_Neuro`, `EXTERNAL_DB_USER`, `EXTERNAL_DB_PASSWORD`,
> `EXTERNAL_DB_ENCRYPT` (disable|true|false), `EXTERNAL_DB_DRIVER=siesa`.

**Sintoma**: El bot arranca igual (no es fatal) pero en logs dice
`external db not available, bot will start in degraded mode` y `GET /health` devuelve
`"status":"degraded"`, `"external_db":"not connected"`. En degraded NO se pueden buscar/crear citas.

**Causas posibles**:
- El SQL Server de SIESA no está corriendo / no es alcanzable desde el servidor
- El firewall bloquea el puerto **1433**
- Host/credenciales incorrectos en `.env`
- Desajuste de TLS: `EXTERNAL_DB_ENCRYPT` no coincide con lo que exige el SQL Server

**Diagnosticar**:
```bash
# Desde el host: ¿hay TCP en 1433 del servidor SIESA?
nc -zv 192.168.1.207 1433        # o: telnet 192.168.1.207 1433

# Probar login con sqlcmd (si está instalado en el host)
sqlcmd -S 192.168.1.207,1433 -U sa -P '<EXTERNAL_DB_PASSWORD>' -d ZeusSalud_Neuro -No -Q "SELECT 1"

# Desde dentro del container: confirmar que alcanza el host:puerto
docker exec neuro_bot sh -c "nc -zv 192.168.1.207 1433" 2>&1 || echo "no alcanza el SQL Server"

# Ver el detalle del fallo en logs del bot
docker compose logs bot --tail 100 | grep -iE "external db|degraded|mssql|login failed|TLS"
```

**Soluciones**:

**a) SQL Server caído o host/credenciales mal**: verificar con el administrador de SIESA que el
servicio está arriba y que el usuario `sa` (o el configurado) tiene acceso a `ZeusSalud_Neuro`.
Confirmar `EXTERNAL_DB_HOST` / `EXTERNAL_DB_USER` / `EXTERNAL_DB_PASSWORD` en `.env`.

**b) Firewall bloquea el 1433**:
```bash
# UFW (si SIESA está en otra máquina de la LAN, el bloqueo suele ser del lado SIESA/red)
sudo ufw allow out to 192.168.1.207 port 1433 proto tcp
# El contenedor usa la red bridge; alcanza IPs de la LAN vía NAT del host (no requiere host.docker.internal).
```

**c) Desajuste de TLS (`login failed` / errores de certificado)**: el SQL Server puede o no exigir
cifrado del canal. Ajustar:
```env
EXTERNAL_DB_ENCRYPT=disable   # sin TLS (como corre hoy en prod)
# EXTERNAL_DB_ENCRYPT=true    # exige TLS — requiere certificado válido/confiable en el SQL Server
```
Si está en `true` y el SQL Server no tiene un certificado confiable, la conexión falla → usar `disable`
mientras se valida el certificado.

**d) Driver**: debe ser `EXTERNAL_DB_DRIVER=siesa` (único soportado). Cualquier otro valor aborta el arranque.

---

### 3. ngrok No Conecta / Tunnel Caido

**Sintoma**: `ERR_NGROK_*` en logs, Bird webhooks no llegan

**Causas posibles**:
- `NGROK_AUTHTOKEN` invalido o vencido
- `NGROK_HOSTNAME` ya en uso por otra instancia
- Limite de conexiones en plan gratuito
- DNS del hostname no propagado

**Diagnosticar**:
```bash
# Ver logs de ngrok
docker compose logs ngrok --tail 50

# Ver si el tunnel esta activo
curl -s http://localhost:14041/api/tunnels | jq .

# Verificar health
docker inspect neuro_bot_ngrok --format='{{.State.Health.Status}}'
```

**Soluciones**:

**a) Token invalido**: Regenerar en https://dashboard.ngrok.com/tunnels/authtokens

**b) Hostname en uso**: Solo UNA instancia puede usar el mismo hostname. Si hay otra instancia corriendo (ej: dev local + produccion), la segunda falla.
```bash
# Verificar en dashboard ngrok que no hay otro tunnel activo
# Si es necesario, cambiar hostname:
NGROK_HOSTNAME=bot-prod.colibrixa.com
```

**c) ngrok caido pero bot sano**: El watcher envia alerta Telegram. Bird reintenta webhooks por ~24h. Al restaurar ngrok, los reintentos de Bird llegan. Con el WAL, cualquier mensaje que haya llegado antes de la caida ya esta persistido.

**d) Produccion sin ngrok**: En un servidor con IP publica y dominio propio, ngrok no es necesario. Usar un reverse proxy:
```yaml
# Reemplazar servicio ngrok con certbot/nginx, o apuntar dominio directo al puerto 8080
# Ejemplo con Caddy (auto-TLS):
# caddy reverse-proxy --from app.colibrixa.com --to localhost:8080
```

---

### 4. MySQL Local No Arranca

**Sintoma**: `neuro_bot_db` en estado `restarting` o `unhealthy`

**Causas posibles**:
- Volume corrupto
- Permisos del directorio de datos
- Disco lleno
- Puerto 13308 ocupado por otro MySQL

**Diagnosticar**:
```bash
# Ver logs de MySQL
docker compose logs db --tail 100

# Verificar estado
docker inspect neuro_bot_db --format='{{.State.Health.Status}}'

# Verificar espacio en disco
df -h

# Verificar permisos del volume (el nombre lleva el prefijo del proyecto)
docker volume ls | grep botdbdata          # descubrir el nombre real (ej. neurobotv2_botdbdata)
docker volume inspect neurobotv2_botdbdata
```

**Soluciones**:

**a) Disco lleno**:
```bash
# Limpiar imagenes Docker sin uso
docker image prune -a

# Limpiar logs de containers
docker system prune --volumes=false  # SIN --volumes para no borrar botdbdata

# Ver peso de logs
du -sh /var/lib/docker/containers/*/
```

**b) Volume corrupto** (ultimo recurso):
```bash
# 1. PRIMERO intentar backup si MySQL arranca aunque sea temporalmente
./scripts/backup-db.sh

# 2. Si no arranca, borrar volume y recrear
docker compose down
VOL=$(docker volume ls --format '{{.Name}}' | grep botdbdata | head -1)  # nombre real con prefijo
docker volume rm "$VOL"               # CUIDADO: esto borra TODOS los datos
docker compose up -d
# 3. Restaurar backup
gunzip -c backups/neuro_bot_YYYY-MM-DD_HHMMSS.sql.gz | docker exec -i neuro_bot_db mysql -ubotuser -pbotpass neuro_bot
```

**c) Otro proceso ocupa el puerto host 13308**:
```bash
# El mapeo es ${DB_EXTERNAL_PORT}:3306. Si 13308 está ocupado, cambiar el puerto HOST:
DB_EXTERNAL_PORT=13309  # en .env (NO DB_PORT, que es el interno 3306)
```

---

### 5. Bot Crashea con OOMKill

**Sintoma**: Container `neuro_bot` se reinicia constantemente, logs muestran `Killed` o `OOMKilled`

**Diagnosticar**:
```bash
# Ver si fue OOM
docker inspect neuro_bot --format='{{.State.OOMKilled}}'

# Ver uso de memoria
docker stats neuro_bot --no-stream

# Ver limites actuales
docker inspect neuro_bot --format='{{.HostConfig.Memory}}'
```

**Causa**: El límite del bot es **1024M** (perfil normal, en `docker-compose.yml`). Go puede excederlo si:
- Muchas goroutines acumuladas (leak)
- Llamadas OpenAI con respuestas muy grandes
- Carga alta (muchos chats simultaneos)

> Nota: el límite de 128M que se ve en `docker stats` para `neuro_bot_ngrok` es del **ngrok**, no del bot.
> Bot=1024M, DB=1024M, ngrok=128M, watcher=64M (límites base reales).

**Soluciones**:

**a) Si es por carga alta** — usar el perfil high-load:
```bash
./scripts/scale-up.sh   # DB sube a 2048M (200 conns, buffer 1G); el bot mantiene 1024M pero con 4 CPU y 50 workers
```

**b) Si es un leak** — aumentar temporalmente mientras se investiga:
```yaml
# En docker-compose.yml
deploy:
  resources:
    limits:
      memory: 2048M  # Subir el bot de 1024M a 2048M
```

**Monitorear**: El capacity monitor envia alertas Telegram automaticamente cuando la carga se acerca a los limites. Tambien se puede revisar manualmente:
```bash
docker stats --format "table {{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}" --no-stream
curl -s http://localhost:8085/health | jq .   # 8085 = PORT de prod (sin jq si no está instalado)
```

---

### 6. Watcher No Envia Alertas Telegram

**Sintoma**: Un container crashea pero no llega alerta Telegram

**Diagnosticar**:
```bash
# Ver logs del watcher
docker compose logs watcher --tail 50

# Verificar que las variables estan configuradas
docker exec neuro_bot_watcher env | grep TG_

# Probar manualmente
curl -s "https://api.telegram.org/bot<TG_BOT_TOKEN>/getMe"
```

**Causas comunes**:
- `TG_BOT_TOKEN` o `TG_CHAT_IDS` vacios en `.env`
- Bot de Telegram no iniciado (el usuario debe enviar `/start` al bot)
- Firewall bloquea `api.telegram.org`

**Solucion**: Verificar que en `.env` estan configurados:
```env
TG_BOT_TOKEN=1234567890:ABCdefGHIjklMNOpqrsTUVwxyz
TG_CHAT_IDS=123456789,987654321
```

---

### 7. Migraciones Fallan al Iniciar

**Sintoma**: Bot no arranca, logs dicen `migration error` o `dirty database`

**Diagnosticar**:
```bash
docker compose logs bot --tail 50 | grep -i migrat
```

**Causas**:
- Una migracion anterior fallo a medias (estado `dirty`)
- Tabla `schema_migrations` tiene `dirty=1`

**Solucion**:
```bash
# Conectar a la BD
mysql -h 127.0.0.1 -P 13308 -u botuser -pbotpass neuro_bot

# Ver estado de migraciones
SELECT * FROM schema_migrations;

# Si dirty=1, limpiar manualmente:
UPDATE schema_migrations SET dirty = 0;

# Verificar que la tabla de la migracion fallida existe/no existe
# y corregir manualmente si es necesario

# Reiniciar bot
docker compose restart bot
```

> **Nota (cambio reciente)**: el seed de las sedes (`center_locations`) ya **NO** vive en
> `docker/mysql/init/` (eliminado); ahora lo aplica la **migración `024_seed_center_locations`**.
> En un volumen nuevo, las migraciones (incluido el seed) corren solas al arrancar el bot. Para ver el
> estado: `make migrate-status` o `SELECT * FROM schema_migrations;`.

---

### 8. Bird Webhooks Llegan Pero Firma Invalida

**Sintoma**: Logs muestran `invalid webhook signature` constantemente

**Causas**:
- `BIRD_WEBHOOK_SECRET` no coincide con el configurado en Bird
- URL reconstruida no coincide con la que Bird usa para firmar
- Proxy/load balancer modifica headers

**Diagnosticar**:
```bash
# Ver que URL reconstruye el bot
docker compose logs bot --tail 100 | grep "invalid webhook"
# El log muestra: url, has_signature, has_timestamp, body_preview
```

**Soluciones**:

**a) Verificar secreto**: En Bird Dashboard > Webhooks, copiar el Signing Key exacto y ponerlo en `BIRD_WEBHOOK_SECRET`

**b) URL mismatch**: Bird firma con la URL exacta configurada en el webhook. Si cambias el hostname de ngrok, debes actualizar tambien en Bird Dashboard.

**c) Outbound webhook**: Tiene su propio signing key. Configurar `BIRD_WEBHOOK_SECRET_OUTBOUND` separado si es diferente.

**d) Voice webhook**: Las llamadas de voz usan `BIRD_WEBHOOK_SECRET_VOICE`. Si esta vacio, usa `BIRD_WEBHOOK_SECRET` como fallback.

**e) Conversations webhook**: El endpoint `/api/webhooks/conversations` usa `BIRD_WEBHOOK_SECRET_CONVERSATIONS`. Si esta vacio, se **omite** la validacion de firma (util en desarrollo pero no recomendado en produccion).

---

### 9. Timezone Incorrecto — Tareas Programadas a Hora Equivocada

**Sintoma**: Los reminders de las 07:00 se envian a las 02:00 (o cualquier desfase)

**Diagnosticar**:
```bash
# Ver timezone del container
docker exec neuro_bot date
docker exec neuro_bot_db date

# Ver timezone del host
date
timedatectl
```

**Causa**: El host no tiene `America/Bogota` y el container hereda algo diferente.

**Solucion**: Ya configurado en docker-compose.yml (`TZ=America/Bogota`), pero verificar que el package `tzdata` esta en el Dockerfile (ya lo esta: `apk add tzdata`).

---

### 10. Espacio en Disco Agotado

**Sintoma**: Containers fallan, logs dicen `no space left on device`

**Diagnosticar**:
```bash
df -h
du -sh /var/lib/docker/

# Ver peso de imagenes
docker system df

# Ver peso de volumes
docker system df -v | head -30
```

**Solucion**:
```bash
# Limpiar imagenes no usadas (SEGURO - no borra volumes)
docker image prune -a

# Limpiar build cache
docker builder prune

# NUNCA usar docker system prune --volumes (borra botdbdata)
```

**Prevencion**: Los logs ya tienen limites (`max-size: 10m`, `max-file: 7` = max 70MB por servicio). El cleanup del scheduler a las 02:00 borra datos antiguos de la BD.

---

### 11. Go Binary No Compatible con la Arquitectura

**Sintoma**: `exec format error` al arrancar el container bot

**Causa**: El Dockerfile compila para `GOOS=linux` pero no especifica `GOARCH`. Si compilas en Mac ARM (M1/M2) y despliegas en Linux x86, falla.

**Solucion**: Especificar arquitectura en el build:
```dockerfile
# En docker/Dockerfile, cambiar la linea de build:
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o neuro-bot ./cmd/server
```

O usar Docker buildx para multi-arch:
```bash
docker buildx build --platform linux/amd64 -t neuro-bot .
```

---

### 12. BD Local Perdida por `docker compose down -v`

**Sintoma**: Despues de recrear containers, la BD esta vacia (solo seeds)

**Causa**: Alguien ejecuto `docker compose down -v` (el flag `-v` elimina volumes)

**Prevencion**:
- El `docker-compose.yml` tiene un comentario de advertencia
- Crear un alias seguro:

```bash
# Agregar a ~/.bashrc o ~/.zshrc del servidor
alias dcdown='docker compose down'
alias dcdownv='echo "PELIGRO: Esto borra la BD. Usa: docker compose down (sin -v)"'
```

**Recuperar**: Restaurar desde backup (ver seccion Backup mas abajo)

---

### 13. Container No Arranca por Dependencias

**Sintoma**: `neuro_bot` en `waiting` porque `neuro_bot_db` no esta healthy

**Diagnosticar**:
```bash
docker compose ps
docker compose logs db --tail 50
```

**Causa**: MySQL tarda en iniciar (especialmente la primera vez con volume vacio). El `start_period: 60s` ya da 60 segundos de gracia.

**Solucion**: Si MySQL necesita mas tiempo (ej: servidor lento):
```yaml
db:
  healthcheck:
    start_period: 120s   # Subir de 60s a 120s
```

---

### 14. Docker Daemon No Arranca / No Instalado

**Diagnosticar**:
```bash
# Verificar que Docker esta instalado
docker --version
docker compose version

# Verificar que el servicio esta corriendo
sudo systemctl status docker

# Si no arranca
sudo systemctl start docker
sudo journalctl -u docker --tail 50
```

**Instalar Docker en Ubuntu/Debian**:
```bash
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker $USER
# Cerrar sesion y volver a entrar
```

---

### 15. Permisos de Docker Socket (Watcher)

**Sintoma**: Watcher no puede monitorear containers

**Causa**: `/var/run/docker.sock` no accesible

**Solucion**: Verificar que el volume esta montado como read-only:
```yaml
volumes:
  - /var/run/docker.sock:/var/run/docker.sock:ro
```

En algunos sistemas con SELinux:
```bash
sudo chcon -t container_runtime_exec_t /var/run/docker.sock
```

---

### 16. "El bot no agenda / no responde / no manda notificaciones" — Kill Switches

Tres variables de entorno pueden **desactivar funcionalidad a propósito**. Si el bot está sano
(health `ok`, sin errores) pero no hace lo esperado, revisarlas ANTES de debuggear código:

| Variable | default | Efecto si está en `false` |
|----------|---------|---------------------------|
| `BOT_ENABLED` | `true` | El bot NO autogestiona: **todo se escala directo a un agente humano**. Útil para apagar la automatización sin tumbar el servicio. |
| `WHATSAPP_NOTIFICATIONS_ENABLED` | `true` | No se envía **ningún template de WhatsApp** (recordatorios, confirmaciones, lista de espera). |
| `IVR_NOTIFICATIONS_ENABLED` | `true` | No se coloca **ninguna llamada IVR**. |

> En el `.env` de producción actual, **`BOT_ENABLED=false`** (la versión vieja escalaba todo a agente).
> Al desplegar la versión nueva y querer autogestión, ponerlo en `true`.

**Diagnosticar**:
```bash
docker exec neuro_bot env | grep -E "BOT_ENABLED|WHATSAPP_NOTIFICATIONS_ENABLED|IVR_NOTIFICATIONS_ENABLED|TESTING_ALWAYS_OPEN"
```

**Relacionado — horario de atención**: si el bot responde "fuera de horario" cuando no debería (o al
revés), revisar `TESTING_ALWAYS_OPEN` (debe ser `false` en prod; `true` bypasea la validación de
horario y atiende 24/7). Ojo con el perfil **high-load**: ya NO arrastra `.env.testing`, pero si el
`.env` del servidor tuviera `TESTING_ALWAYS_OPEN=true` quedaría 24/7.

---

### 17. Usuario que SIESA registra como autor de las citas

Las citas que crea/cancela el bot quedan atribuidas a un usuario de SIESA configurable:
- `SIESA_ASSIGN_USER_CEDULA` → columna `cod_user_asigna_cita` (cédula). Prod: SHERNANDEZ.
- `SIESA_ASSIGN_USER_ID` → `usuario.id` (usuario_evento / id_usuario_cancela).
- Si no se definen, caen al usuario de automatización "Procesos Automáticos" (`000000` / `10006`).

Si en SIESA las citas del bot aparecen sin autor o con el usuario equivocado, verificar estas dos
variables en el `.env` del servidor.

---

## Backup de la BD Interna

> Esto es **solo la BD local del bot** (`neuro_bot`: sesiones, eventos, lista de espera, catálogo
> CUPS, WAL). La **BD clínica SIESA es externa** y la respalda el área de sistemas de la IPS, no el bot.

**Resumen — responde "¿cada cuánto y dónde?":**

| Pregunta | Respuesta |
|----------|-----------|
| ¿Cada cuánto se hace? | **NO es automático.** Hay que configurar un **cron** (recomendado: diario a las **03:00**). Sin cron, solo corre cuando lo ejecutas a mano. El bot NO hace backup solo (su tarea de 02:00 es *limpieza* de datos, no dump). |
| ¿Cada cuánto se "actualiza"? | Cada corrida genera un **dump nuevo y completo** (snapshot). Con el cron diario → un backup fresco por día. |
| Retención | Se borran solos los dumps de **más de 30 días** (`find -mtime +30 -delete` dentro del script). |
| ¿Dónde se almacena en prod? | En **`<carpeta_del_proyecto>/backups/`** (ej. `/ruta/a/Neurobotv2/backups/`), en el **disco local del servidor**. Está en `.gitignore` (no se sube a git). |
| ⚠️ Riesgo | Los backups quedan en el **mismo disco/servidor** que la BD. Si el disco/servidor muere, se pierden BD **y** backups. **Recomendado: copiarlos off-site** (otro disco, NAS, S3, `rsync`/`scp` a otra máquina). |

### Script de Backup

Ya existe en `scripts/backup-db.sh`. Genera dumps comprimidos en `backups/` (usa `--no-tablespaces`
para funcionar con el usuario restringido en MySQL 8.0).

```bash
# Ejecutar manualmente
cd /ruta/a/neuro-bot
./scripts/backup-db.sh

# Output: backups/neuro_bot_2026-03-07_153000.sql.gz
```

### Configurar o confirmar el Cron (un solo comando)

Ejecutar **desde la carpeta del proyecto**. Es idempotente: agrega la línea del backup diario 03:00
solo si no existe, y luego muestra el crontab para confirmar:

```bash
# CONFIGURAR (idempotente) + CONFIRMAR
( crontab -l 2>/dev/null | grep -v 'backup-db.sh'; \
  echo "0 3 * * * cd $(pwd) && ./scripts/backup-db.sh >> $(pwd)/backups/cron.log 2>&1" ) | crontab - \
  && crontab -l | grep backup-db.sh && echo "✓ cron de backup configurado"
```

Para **solo confirmar** que ya está (sin tocar nada):
```bash
crontab -l | grep backup-db.sh || echo "✗ NO configurado"
```

### Verificar que el Cron Funciona

```bash
# Ver crontab activo
crontab -l

# Ejecutar manualmente para probar
cd /ruta/a/neuro-bot && ./scripts/backup-db.sh

# Ver log despues de las 03:00
tail -20 /var/log/neuro-bot-backup.log

# Ver backups generados
ls -lh backups/
```

### Restaurar un Backup

```bash
# Listar backups disponibles
ls -lh backups/

# Restaurar (CUIDADO: sobreescribe datos actuales)
gunzip -c backups/neuro_bot_2026-03-07_030000.sql.gz | \
  docker exec -i neuro_bot_db mysql -ubotuser -pbotpass neuro_bot
```

### Backup Antes de Operaciones Peligrosas

Siempre hacer backup antes de:
- `docker compose down` (por si accidentalmente se agrega `-v`)
- Actualizar version de MySQL
- Ejecutar migraciones manuales
- Cualquier cambio en la estructura de la BD

```bash
# Backup rapido antes de operacion peligrosa
./scripts/backup-db.sh && docker compose down
```

---

## Comandos de Diagnostico Rapido

```bash
# Estado general de todos los servicios
docker compose ps

# Uso de recursos en tiempo real
docker stats --format "table {{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}\t{{.NetIO}}"

# Health check del bot (verifica ambas BDs)
curl -s http://localhost:8085/health | jq .   # 8085 = PORT de prod (sin jq si no está instalado)

# Logs de los ultimos 30 minutos (solo errores)
docker compose logs --since 30m 2>&1 | grep -i error

# Ver si algun container fue OOMKilled
for c in neuro_bot neuro_bot_db neuro_bot_ngrok neuro_bot_watcher; do
  echo "$c: $(docker inspect $c --format='{{.State.OOMKilled}}')"
done

# Ver reincios recientes
docker compose ps --format "table {{.Name}}\t{{.Status}}"

# Dashboard ngrok (ver tunnel activo)
curl -s http://localhost:14041/api/tunnels | jq '.tunnels[0].public_url'

# Verificar conectividad a la BD externa SIESA (SQL Server, 1433) desde el container
docker exec neuro_bot sh -c "nc -zv 192.168.1.207 1433" 2>&1; echo "exit: $?"

# Ver perfil de scaling activo (en logs del bot)
docker logs neuro_bot 2>&1 | grep "capacity monitor started"

# Escalar si es necesario
./scripts/scale-up.sh    # Activar high-load
./scripts/scale-down.sh  # Volver a normal
```

---

## Script de Validacion Pre-Deploy

Antes de construir Docker, ejecutar el script que verifica automaticamente que todo este listo:

```bash
cd /ruta/a/neuro-bot
./scripts/pre-deploy-check.sh
```

El script valida 11 categorias:

| # | Categoria | Que verifica |
|---|-----------|-------------|
| 1 | Docker Engine | Docker y Compose instalados y corriendo |
| 2 | Archivos | Dockerfile, docker-compose.yml, migraciones (incluye 024 seed de sedes) |
| 3 | .env | Variables criticas llenas, passwords no son defaults |
| 4 | Puertos | PORT (8080/8085), 13308, 14041 disponibles (o en uso por nuestro container) |
| 5 | BD Externa | Conexion al **SQL Server de SIESA** (puerto 1433), base `ZeusSalud_Neuro` |
| 6 | Recursos | Disco (<80%), RAM disponible (bot+db ~2GB de límites; high-load db sube a 2GB) |
| 7 | Docker Socket | /var/run/docker.sock accesible (para watcher) |
| 8 | Timezone | Host en America/Bogota (afecta cron backup) |
| 9 | Volumes | Detecta si hay volume previo con datos |
| 10 | Ngrok | Token configurado, DNS del hostname |
| 11 | Watcher | Directorio y Dockerfile presentes |

**Resultado**:
- `[OK]` = Todo bien
- `[WARN]` = Funciona pero deberia revisarse
- `[FAIL]` = Debe corregirse antes de construir. Muestra exactamente que comando ejecutar.

El script retorna exit code = numero de FAILs (0 = todo OK).

---

## Perfiles de Escalamiento (Normal / High-Load)

El bot soporta dos perfiles de carga, intercambiables sin cambiar codigo:

| Parametro | Normal | High-Load |
|-----------|--------|-----------|
| Workers (`WORKER_POOL_SIZE`) | 10 | 50 |
| Queue buffer (`WORKER_QUEUE_SIZE`) | 100 | 500 |
| BD local conns (open/idle) | 25/10 | 50/25 |
| BD externa SIESA conns (open/idle) | 10/5 | 50/25 |
| MySQL local max_connections | 50 | 200 |
| Bot RAM (límite) | 1024M | 1024M (sube a 4 CPU) |
| DB local RAM (límite) | 1024M | 2048M (buffer 1G) |
| Capacidad estimada | ~200 chats/h | ~1000 chats/h |

> El perfil high-load **no aumenta la RAM del bot** (queda en 1024M); lo que escala es CPU (2→4),
> workers (10→50), conexiones y los recursos de la BD local. El overlay carga `.env` + `.env.high-load`
> (NO `.env.testing`).

### Escalar a High-Load

```bash
./scripts/scale-up.sh
```

Reconstruye los contenedores con `docker-compose.high-load.yml` + `.env.high-load`. Hace rolling restart (DB primero, luego bot) con health checks.

> **⚠️ Requisito de CPU**: el perfil high-load fija el bot en `cpus: '4.0'`. Si **Docker** puede
> asignar **menos de 4 CPUs**, `scale-up.sh` falla al recrear el bot con:
> `Error response from daemon: range of CPUs is from 0.01 to 2.00, as there are only 2 CPUs available`.
> En ese caso el **DB sí escala** (queda en high-load) pero el bot no arranca. Verificar con
> **`docker info --format '{{.NCPU}}'`** (NO `nproc`: en Docker Desktop el host puede tener más cores
> que los asignados a la VM de Docker). Solución: asegurar ≥4 CPUs en Docker, o bajar `cpus` del bot
> en `docker-compose.high-load.yml`. (Validado: host `nproc=6` pero Docker con `NCPU=2` → falla así;
> el DB aplica `max_connections=200` igual.)

### Volver a Normal

```bash
./scripts/scale-down.sh
```

Reinicia con la configuracion base (`docker-compose.yml` + `.env`).

### Capacity Monitor (Alertas Telegram)

El bot incluye un monitor automatico que cada 30 segundos revisa:
- **Cola de mensajes** — % de llenado del queue buffer
- **Conexiones BD local** — % de conexiones en uso
- **Conexiones BD externa** — % de conexiones en uso

**Alertas de escalar UP** (cualquier metrica >80%):
- Envia alerta urgente a Telegram con metricas y comando `./scripts/scale-up.sh`
- Cooldown de 15 minutos entre alertas

**Sugerencia de escalar DOWN** (solo en perfil high-load):
- Si todas las metricas estan por debajo del 20% por 30+ minutos
- Envia sugerencia con comando `./scripts/scale-down.sh`

**Requisito**: Las variables `TELEGRAM_BOT_TOKEN` y `TELEGRAM_CHAT_ID` deben estar configuradas en `.env`. Sin ellas, el monitor no se activa.

---

## Checklist de Despliegue en Servidor Nuevo

```
[ ] Docker y Docker Compose instalados
[ ] Clonar repositorio: git clone ...
[ ] Copiar .env.example a .env
[ ] Configurar TODAS las variables en .env (ver docs/env-production.env como referencia):
    [ ] PORT (prod usa 8085)
    [ ] EXTERNAL_DB_HOST (192.168.1.207) / EXTERNAL_DB_USER / EXTERNAL_DB_PASSWORD
    [ ] EXTERNAL_DB_DRIVER=siesa / EXTERNAL_DB_ENCRYPT (disable|true)
    [ ] SIESA_ASSIGN_USER_CEDULA / SIESA_ASSIGN_USER_ID (usuario autor de las citas)
    [ ] BIRD_API_KEY_WA / BIRD_ACCESS_KEY_ID / BIRD_WEBHOOK_SECRET
    [ ] BIRD_WEBHOOK_SECRET_OUTBOUND
    [ ] BIRD_WORKSPACE_ID / BIRD_CHANNEL_ID / BIRD_TEAM_FALLBACK
    [ ] BIRD_API_KEY_VOICE / BIRD_VOICE_FLOW_ID (IVR)
    [ ] OPENAI_API_KEY / OPENAI_MODEL
    [ ] INTERNAL_API_KEY (generar con: openssl rand -hex 32)
    [ ] NGROK_AUTHTOKEN / NGROK_HOSTNAME
    [ ] TG_BOT_TOKEN / TG_CHAT_IDS (alertas watcher)
    [ ] TELEGRAM_BOT_TOKEN / TELEGRAM_CHAT_ID (alertas bot + capacity monitor)
    [ ] DB_ROOT_PASSWORD (cambiar de 'secret' a algo seguro)
    [ ] DB_PASSWORD (cambiar de 'botpass' a algo seguro)
    [ ] Kill switches: BOT_ENABLED, WHATSAPP_NOTIFICATIONS_ENABLED, IVR_NOTIFICATIONS_ENABLED
    [ ] TESTING_ALWAYS_OPEN=false (NO 24/7 en prod)
[ ] Ejecutar validacion: ./scripts/pre-deploy-check.sh
[ ] Corregir todos los [FAIL] reportados
[ ] SQL Server de SIESA (ZeusSalud_Neuro, puerto 1433) accesible desde el servidor
[ ] Timezone del servidor: America/Bogota (timedatectl set-timezone America/Bogota)
[ ] Docker auto-start al boot: sudo systemctl enable docker
[ ] Levantar servicios: docker compose up -d --build
[ ] Verificar health: curl http://localhost:8085/health   # PORT de prod
[ ] Verificar todos healthy: docker compose ps
[ ] Verificar tunnel ngrok: curl http://localhost:14041/api/tunnels
[ ] Configurar webhooks en Bird Dashboard con la URL de ngrok
[ ] Configurar cron de backup (ver seccion arriba)
[ ] Enviar mensaje de prueba por WhatsApp
[ ] Verificar que llegan alertas Telegram (reiniciar un container de prueba)
[ ] Probar scaling: ./scripts/scale-up.sh && ./scripts/scale-down.sh
```
