# Escalamiento en Producción (Subir / Bajar Capacidad)

Runbook paso a paso para cambiar la capacidad de los contenedores Docker del bot entre el perfil
**normal** (~200 chats/h) y el perfil **high-load** (~1000 chats/h), sin tocar código.

> Probado el 2026-06-26 (levantado desde cero + scale-up + scale-down).

---

## ⚠️ Requisito previo (LEER ANTES)

El perfil high-load fija el **bot en `cpus: 4.0`**. **Docker debe poder asignar ≥ 4 CPUs.**
Si no, `scale-up.sh` falla al recrear el bot con:

```
Error response from daemon: range of CPUs is from 0.01 to 2.00, as there are only 2 CPUs available
```

En ese caso el **DB sí escala** pero el **bot no arranca**. Solución: bajar `cpus` del bot en
`docker-compose.high-load.yml` al número disponible (ej. `cpus: '2.0'`) antes de escalar.

Verificar las CPUs que **Docker** puede asignar (este es el número que importa, NO `nproc`):
```bash
docker info --format '{{.NCPU}}'   # ← AUTORITATIVO: CPUs disponibles para Docker
nproc                              # cores del HOST (puede ser MAYOR que el de arriba)
```

> ⚠️ En **Docker Desktop** (Windows/Mac) Docker corre en una VM con un nº de CPUs configurable
> (Settings → Resources → CPUs); ese límite (`docker info NCPU`) puede ser **menor** que `nproc` del
> host. Ejemplo real: host con `nproc=6` pero Docker Desktop con `NCPU=2` → `cpus: 4.0` falla aunque
> "haya 6 cores". En un **servidor Linux** (Docker nativo, sin VM) ambos coinciden. El `cpus: 4.0`
> del high-load se compara siempre contra `docker info NCPU`.

---

## Qué cambia en cada perfil

| Parámetro | Normal | High-Load |
|-----------|--------|-----------|
| Workers (`WORKER_POOL_SIZE`) | 10 | 50 |
| Queue buffer (`WORKER_QUEUE_SIZE`) | 100 | 500 |
| Conns BD local (open/idle) | 25/10 | 50/25 |
| Conns BD SIESA (open/idle) | 10/5 | 50/25 |
| MySQL local `max_connections` | 50 | 200 |
| Bot CPU | 2.0 | 4.0 |
| DB RAM (límite) | 1024M | 2048M (buffer 1G) |

> El bot mantiene 1024M de RAM en ambos perfiles; lo que sube es CPU, workers, conexiones y los
> recursos del DB.

---

## SUBIR capacidad (Normal → High-Load)

```bash
# 1. Ir a la carpeta del proyecto (donde está docker-compose.yml)
cd /ruta/al/proyecto

# 2. Ejecutar el script
./scripts/scale-up.sh
```

El script hace **rolling restart con health checks**:
1. Reconstruye la imagen del bot con el overlay high-load.
2. Recrea el **DB** primero (espera a que esté `healthy`).
3. Recrea el **bot** (espera a que esté `healthy`).
4. Muestra `docker compose ps`.

### Verificar que quedó en high-load

```bash
# Todos healthy
docker compose ps

# Workers = 50
docker logs neuro_bot 2>&1 | grep "worker pool started" | tail -1

# max_connections = 200
docker exec neuro_bot_db mysql -uroot -p"$DB_ROOT_PASSWORD" \
  -e "SHOW VARIABLES LIKE 'max_connections';"

# Health OK (usar el PORT de prod, ej. 8085)
curl -s http://localhost:8085/health
```

---

## BAJAR capacidad (High-Load → Normal)

```bash
cd /ruta/al/proyecto
./scripts/scale-down.sh
```

Reinicia con la configuración base (`docker-compose.yml` + `.env`).

### Verificar que volvió a normal

```bash
docker logs neuro_bot 2>&1 | grep "worker pool started" | tail -1   # workers = 10

docker exec neuro_bot_db mysql -uroot -p"$DB_ROOT_PASSWORD" \
  -e "SHOW VARIABLES LIKE 'max_connections';"                       # = 50

curl -s http://localhost:8085/health
```

---

## Alternativa manual (sin los scripts)

```bash
# Subir a high-load
docker compose -f docker-compose.yml -f docker-compose.high-load.yml up -d --build

# Volver a normal
docker compose up -d --build
```

> El overlay high-load carga `.env` + `.env.high-load` (NO `.env.testing`). El perfil de pruebas es
> `docker-compose.testing.yml`, aparte.

---

## ¿Cuándo escalar? (Capacity Monitor)

El bot trae un monitor que cada 30s revisa cola y conexiones y **avisa por Telegram** (no escala solo):

- **Subir**: si cualquier métrica supera **80%** → alerta urgente con el comando `./scripts/scale-up.sh`
  (cooldown 15 min entre alertas).
- **Bajar**: si en high-load todas las métricas están **<20% por 30+ min** → sugiere `./scripts/scale-down.sh`.

Requiere `TELEGRAM_BOT_TOKEN` y `TELEGRAM_CHAT_ID` en el `.env`. Sin ellas, el monitor no se activa.

---

## Notas

- `scale-up.sh` exige que existan `docker-compose.high-load.yml` y `.env.high-load` (aborta si faltan).
- El cambio es **sin downtime apreciable** (rolling restart DB→bot con health checks), pero hay unos
  segundos de recreación de contenedores.
- No borra datos: NO usa `down -v`. El volumen `botdbdata` se conserva.
- Para diagnóstico de fallos al escalar, ver `TROUBLESHOOTING-PROD.md` (sección
  "Perfiles de Escalamiento" y el caveat de CPU).
