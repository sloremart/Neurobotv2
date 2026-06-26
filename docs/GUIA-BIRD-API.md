# Guía de la API de Bird (auditar con registros directos de Bird)

> Cómo consultar la API de Bird con las credenciales del `.env` para **cruzar los hallazgos del bot
> contra la fuente de verdad de Bird**: qué envió/recibió realmente un paciente, estado de entrega de
> mensajes, conversaciones y — clave — las **suscripciones de webhook** (firmas/duplicadas).
> Complementa [`GUIA-AUDITORIA.md`](GUIA-AUDITORIA.md). **Todo lo de aquí es de LECTURA (GET).**

---

## 1. Acceso

- **Base:** `https://api.bird.com`
- **Auth:** header `Authorization: AccessKey <token>`. Funcionan dos tokens del `.env`:
  - `BIRD_ACCESS_KEY_ID` → usado por el bot para **leer** conversaciones; con él pasaron TODOS los GET de abajo.
  - `BIRD_API_KEY_WA` → usado por el bot para **enviar**; también es un AccessKey válido.
- **IDs del `.env`:** `BIRD_WORKSPACE_ID` (`{wid}`), `BIRD_CHANNEL_ID` (`{cid}`, canal de prod `61a8f3cc…`).

Helper (lee credenciales del `.env`, sin imprimirlas):
```bash
g(){ grep -E "^$1=" .env | head -1 | sed -E 's/^[^=]+=//; s/[[:space:]]+#.*$//; s/[[:space:]]*$//'; }
AK=$(g BIRD_ACCESS_KEY_ID); WID=$(g BIRD_WORKSPACE_ID); CID=$(g BIRD_CHANNEL_ID)
B="https://api.bird.com"; H="Authorization: AccessKey $AK"
# uso:  curl -s -H "$H" "$B/workspaces/$WID/..."
```
> No hay `jq` en el entorno; usa `python3 -c` (leyendo de stdin) para formatear/filtrar.

**Paginación:** las listas devuelven `nextPageToken`. Para la siguiente página: añade `&pageToken=<token>`.

---

## 2. Endpoints que FUNCIONAN (GET, verificados → 200)

| Endpoint | Devuelve | Uso para auditar |
|----------|----------|------------------|
| `/workspaces/{wid}/channels` | Lista de canales: `id, status, platformId, name, connectorId, identifier` | Ver qué canales hay (prod `61a8f3cc`, pruebas `dacc4070`) y su número. |
| `/workspaces/{wid}/channels/{cid}` | Detalle del canal | Confirmar nombre/número/estado del canal de prod. |
| `/workspaces/{wid}/webhook-subscriptions` | Lista de suscripciones: `id, service, event, eventFilters[], url, status, createdAt, updatedAt` ⭐ | **Investigar firmas/duplicadas:** cuántas suscripciones hay por evento/canal y a qué URL apuntan. |
| `/workspaces/{wid}/webhook-subscriptions/{id}` | Detalle de una suscripción | Ver `event`, `url`, `status`, `eventFilters` de una en concreto. (El **signing key NO se expone**.) |
| `/workspaces/{wid}/conversations?channelId={cid}&status=active&limit=N` | Lista de conversaciones: `id, status, featuredParticipants, channelId, lastMessage, lastMessageIncomingAt/OutgoingAt, createdAt…` | Encontrar el hilo de un paciente; ver si hay varios hilos por teléfono (causa de "responde al hilo viejo" §13.3). |
| `/workspaces/{wid}/conversations/{convId}` | Detalle de la conversación | Estado, participantes, último mensaje. |
| `/workspaces/{wid}/conversations/{convId}/messages?limit=N` | Mensajes de ESA conversación | Reconstruir el hilo completo (entrantes y salientes) como lo ve Bird. |
| `/workspaces/{wid}/channels/{cid}/messages?limit=N` | Mensajes del canal: `id, sender, receiver, body{type,text}, status, reason, direction, createdAt, lastStatusAt…` | Ver el flujo de mensajes del canal. |
| `/workspaces/{wid}/channels/{cid}/messages?phonenumber=%2B57XXXXXXXXXX&limit=N` | Igual, **filtrado por teléfono** | **Lo que un paciente REALMENTE envió/recibió** según Bird (cruzar contra lo que procesó el bot). `%2B` = `+`. |
| `/workspaces/{wid}/channels/{cid}/messages/{msgId}` | Detalle de UN mensaje (incluye `status`: sent/delivered/read/failed, `direction`, `reason`) | Confirmar si un envío del bot se **entregó** o **falló** del lado de WhatsApp. |
| `/workspaces/{wid}/contacts?limit=N` | Contactos: `id, computedDisplayName, featuredIdentifiers, attributes…` | Resolver nombre/identificadores de un teléfono. |

### NO accesibles con esta key (→ 403)
- `/workspaces/{wid}` (raíz del workspace)
- `/workspaces/{wid}/channels/{cid}/webhook-subscriptions` (usar el de nivel workspace de arriba)
- `/workspaces/{wid}/event-subscriptions`

> Existen también endpoints de **escritura** (crear/actualizar/borrar suscripciones, enviar mensajes,
> etc.). **NO se documentan ni se usan aquí**: auditar es solo lectura. Mutar webhooks en prod puede
> tumbar la entrega.

---

## 3. Recetas de auditoría (cruzar bot ↔ Bird)

**a) ¿Qué envió realmente un paciente? (vs lo que procesó el bot)**
```bash
curl -s -H "$H" "$B/workspaces/$WID/channels/$CID/messages?phonenumber=%2B573103343616&limit=10" \
 | python3 -c 'import sys,json;[print(m["createdAt"],m["direction"],m["status"],json.dumps(m.get("body",{}).get("text",{}))) for m in json.load(sys.stdin)["results"]]'
```
Compara con `/api/internal/events?phone=` del bot: si Bird tiene mensajes del paciente que el bot NO
procesó → el bot los perdió (firma/whitelist/backpressure).

**b) ¿El bot respondió pero el paciente no lo vio? (estado de entrega)**
Toma el `bird_msg_id` de un `message_sent_ok` del bot → `GET …/channels/{cid}/messages/{msgId}` →
mira `status` (`delivered`/`read` = llegó; `failed` = no).

**c) Investigar firmas/suscripciones duplicadas (§13.8 de la guía de auditoría)**
```bash
curl -s -H "$H" "$B/workspaces/$WID/webhook-subscriptions" \
 | python3 -c 'import sys,json
r=json.load(sys.stdin)["results"]
for s in r:
    ch=next((f["value"] for f in s.get("eventFilters",[]) if f["key"]=="channelId"),"?")
    print(s["event"], ch[:8], s["url"], s["status"])'
```
Si para el MISMO `event`+canal hay **dos URLs** o dos entradas → duplicada (cada una firma con su propia
llave; el bot solo valida `BIRD_WEBHOOK_SECRET`). **Hallazgo 2026-06-26:** prod (`app.colibrixa.com`,
canal `61a8f3cc`) tiene **exactamente 3 suscripciones** (`whatsapp.inbound`, `conversation.created`,
`whatsapp.outbound`), **sin duplicados** → el fallo parcial de firma NO es por suscripción duplicada.

**d) ¿Un teléfono tiene varios hilos activos? (causa de "responde al hilo viejo" §13.3)**
```bash
curl -s -H "$H" "$B/workspaces/$WID/conversations?channelId=$CID&limit=50" \
 | python3 -c 'import sys,json
for c in json.load(sys.stdin)["results"]:
    ps=[p.get("contact",{}).get("identifierValue") or p.get("identifierValue") for p in c.get("featuredParticipants",[])]
    print(c["id"], c["status"], ps)'
```
Filtra por el teléfono del paciente: si aparece en >1 conversación activa, el bot puede agarrar el hilo
equivocado al responder.

---

## 4. ¿La llave de firma (webhook) está bien configurada?

La API **no expone el signing key** de cada suscripción → no se compara directo. Pero se prueba así:

**Prueba 1 (empírica):** ¿el bot valida ALGÚN mensaje? Si en logs hay `processing message` /
`greeting_sent` / bookings → la firma validó → **la llave es correcta**. Una llave mal configurada falla
el **100%** (todos 401), no parcial.

**Prueba 2 (decisiva):** ¿hay firmas REALMENTE malas? El bot loguea un DEBUG `webhook signature mismatch`
(con `expected_b64`/`actual_b64`/`secret_len`) **solo** cuando el HMAC no coincide (tras pasar el chequeo
de edad). Requiere `LOG_LEVEL=debug`.
```bash
curl -s -G -H "X-API-Key: $KEY" --data-urlencode 'search=webhook signature mismatch' \
     "$BASE/api/internal/logs?lines=200" | grep -c "signature mismatch"
```
- `0` → **ninguna firma mala → la llave está bien.**
- `>0` → hay mismatch real → revisar `BIRD_WEBHOOK_SECRET` vs la suscripción.

> **⚠️ Gotcha que confunde (verificado 2026-06-26):** el WARN `invalid webhook signature` se emite
> **también** cuando el `timestamp` del webhook tiene **más de 15 min** (`maxTimestampAge`, anti-replay),
> ANTES de comparar la firma. Cuando Bird **REINTENTA** un webhook que falló antes (caída, whitelist,
> túnel), lo reenvía con el timestamp original viejo → >15 min → rechazado por **edad** y logueado como
> "invalid webhook signature" aunque la firma sea correcta. **Señal inequívoca:** muchos
> `invalid webhook signature` con `has_signature:true` pero **CERO** `webhook signature mismatch` =
> **reintentos viejos, NO problema de llave** (la llave está bien). Esos mensajes viejos sí se perdieron;
> los nuevos entran normal. Bird deja de reintentar a las ~24h.

**Prueba 3 (recompute de UN mensaje):** captura el body crudo + headers `MessageBird-Signature` y
`MessageBird-Request-Timestamp`, y recomputa:
`firma = base64( HMAC-SHA256( secret, timestamp + "\n" + url + "\n" + SHA256_raw(body) ) )`
(el SHA256 del body se concatena como **32 bytes crudos**, no hex; ver `signSha256` en
`internal/bird/webhook.go`). Compara con el header. (Requiere capturar la request cruda → debug temporal.)

---

## 5. Notas
- El **signing key** de cada suscripción **no se expone** por la API (por seguridad) → no se puede
  comparar el secreto del `.env` contra Bird directamente; se infiere por el comportamiento (validan/401).
- Auth con `BIRD_ACCESS_KEY_ID` cubrió todos los GET de lectura. Si alguno da 403, prueba con
  `BIRD_API_KEY_WA`; si sigue 403, esa ruta requiere otro scope (no disponible con estas keys).
- Todo es **lectura**. Para cualquier cambio en Bird (webhooks, etc.) usar el dashboard de Bird con
  cuidado, no esta vía.
