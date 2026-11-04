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
