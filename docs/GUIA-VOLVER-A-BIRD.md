# Guía: volver a Bird

Para la persona de turno que recibe en Telegram el aviso **«🚨 URGENTE — volver a Bird»**. No hace
falta saber programar: son cambios en el `.env` del servidor y un reinicio del bot.

Mientras dura la transición el bot atiende dos líneas: la de siempre (Bird) y la nueva (Néctar). Los
recordatorios y avisos salen por la nueva. Si la nueva falla, esta guía devuelve TODO a Bird.

---

## 1. Antes de cambiar nada: ¿es una falsa alarma? (2-5 minutos)

El aviso dice el **disparador**, un **detalle** (cuántos fallos y desde qué hora) y la **hora**.

| Disparador (lo que dice el aviso) | Qué significa | Cómo confirmarlo |
|---|---|---|
| **Néctar no responde** (`nectar_down`) | Varios envíos o lecturas a Néctar fallaron por red, tiempo vencido o error del servidor de Néctar (5xx) en pocos minutos (por defecto 5 en 5 min). | Abrir el panel web de Néctar: ¿carga?, ¿los agentes ven las conversaciones? Si NO carga, es real. |
| **Plantillas proactivas detenidas por configuración** (`proactive_stopped`) | Néctar rechazó una plantilla por algo de la cuenta (clave, plantilla, canal). La corrida se detuvo: no salió nada más en esa corrida. | Casi siempre es real: el error se repite con cualquier paciente. |
| **Recordatorios sin enviar por encima del umbral** (`reminders_unsent`) | Una corrida de recordatorios dejó muchos (por defecto 10 o más) sin enviar o «en duda» por fallas de la línea. No cuentan los números sin WhatsApp ni los pacientes que ya hablan con un agente. | Es real si el detalle trae un número alto o si se repite en la siguiente corrida. |
| **Escalaciones a Néctar fallando** (`escalation_failing`) | Al pasar pacientes a un agente de Néctar, Néctar los rechazó varias veces seguidas (por defecto 3 en 10 min) por algo que no es una caída (permiso, clave, escalación apagada). | Preguntar a un agente de Néctar si le están llegando pacientes del bot. |

Comprobaciones rápidas en el servidor (carpeta del proyecto):

```bash
# ¿El bot está vivo? (8085 es el PORT del .env de producción; si es otro, usar ese)
curl -s http://localhost:8085/health

# ¿Qué está fallando? Últimos 15 minutos de errores relacionados con Néctar
docker compose logs bot --since 15m | grep -iE "ctar|aviso urgente|plantillas proactivas|recordatorios en duda"
```

**Señales de falsa alarma:**

- El panel de Néctar carga, los agentes trabajan normal y en los logs ya no aparecen errores nuevos.
- El aviso **no se repite**: mientras el problema sigue, el mismo disparador vuelve a avisar cada 10
  minutos (por defecto). Si pasaron 15 minutos sin otro aviso igual, el problema probablemente paró.
- `/health` dice `"status":"critical"` o `"local_db":"error"`: el problema es del bot o de su base
  de datos, NO de Néctar. Volver a Bird no lo arregla; avisar al equipo técnico.

**Si hay duda, cambiar a Bird.** Volver a Néctar después es igual de fácil (paso 6).

---

## 2. Cambiar a Bird

1. Entrar al servidor y a la carpeta del proyecto (la misma donde se hace `git pull`).
2. Hacer una copia del `.env`:
   ```bash
   cp .env .env.antes-de-bird
   ```
3. Abrir el `.env` (`nano .env`) y cambiar SOLO esto:
   - `NECTAR_BASE_URL=` → dejarla **vacía** (anotar el valor que tenía: se necesita para volver).
   - `NEW_LINE_ANNOUNCEMENT=` → dejarla **vacía**. Si no, el saludo de la línea de siempre seguiría
     mandando a los pacientes a la línea nueva, que es justo la que falla.
   - Las demás variables `NECTAR_*` se dejan como están: sin `NECTAR_BASE_URL` no se usan, y así
     volver es solo poner la URL otra vez.
4. Guardar y reiniciar el bot para que lea el `.env` nuevo:
   ```bash
   docker compose up -d bot
   ```
   > **Ojo:** `docker compose restart bot` NO sirve aquí: reinicia con las variables viejas.
   > Si el servidor corre en el perfil de alta carga (`./scripts/scale-up.sh`), usar:
   > `docker compose -f docker-compose.yml -f docker-compose.high-load.yml up -d bot`

---

## 3. Verificar que quedó en Bird

```bash
docker compose ps                 # neuro_bot debe quedar "healthy" (tarda unos segundos)
curl -s http://localhost:8085/health
```

- En `/health`, `nectar_host` debe decir **`"none"`**.
- En los logs del arranque NO debe aparecer «Néctar activo»:
  ```bash
  docker compose logs bot --since 5m | grep -i "ctar activo"     # no debe mostrar nada
  ```
- Desde ese momento no llegan más avisos urgentes: solo existen con la línea nueva encendida.

---

## 4. Qué pasa con lo que estaba abierto en Néctar

**Conversaciones de la línea nueva.** El bot ya no las atiende: no puede responderles por Bird.
Cualquier respuesta del bot a esas conversaciones se corta antes de mandarse (no se paga ningún
mensaje). Si Néctar sigue funcionando y un paciente escribe a la línea nueva, el mensaje puede
llegarle al bot, pero el paciente no recibe ninguna respuesta: **los agentes de Néctar deben atender
esa línea** mientras tanto, y a quien escriba le envían este texto:

> Hola, por un inconveniente técnico en esta línea no podemos atenderte por aquí en este momento.
> Por favor escríbenos a nuestra línea de WhatsApp de siempre, donde te atendemos con normalidad
> para agendar, confirmar o reprogramar tu cita. Disculpa las molestias.

Los agentes pueden cerrar esas conversaciones después de enviar el texto, o dejarlas abiertas.

**Recordatorios de mañana que ya salieron por la línea nueva y el paciente no ha contestado.** Siguen
yendo a la **llamada automática (IVR) de las 13:00**, que sale por Bird y marca al teléfono del
paciente. En la llamada el paciente oprime 1 para confirmar o cualquier otra tecla para cancelar; no
hay que hacer nada.

- Si el paciente oprime un botón del recordatorio en la línea nueva DESPUÉS del cambio:
  - con Néctar caído, el bot no se entera: queda para la llamada de las 13:00 (si el cambio fue antes
    de esa hora) o para el call center;
  - con Néctar funcionando, el bot sí puede registrarlo (por ejemplo, dejar la cita confirmada), pero
    el paciente no ve la respuesta del bot. Si pregunta, el agente le confirma en el sistema.
- Los recordatorios del **mismo día** (los que salen cada hora) no tienen llamada: los que quedaron
  sin respuesta no se recuperan solos.

---

## 5. Recordatorios y avisos desde ahora

Con la línea nueva apagada, los recordatorios del día antes (07:00), los del mismo día, la lista de
espera y los avisos de cancelación salen **por las plantillas de Bird de siempre**, sin tocar nada
más.

Si el aviso fue en plena corrida de recordatorios, los pacientes que quedaron sin recordatorio
**no se vuelven a enviar por WhatsApp**: el bot los dejó apuntados para la llamada de las 13:00. No
hace falta relanzar la corrida.

Para revisar la siguiente corrida (por ejemplo, la de las 07:00 del día siguiente):

```bash
docker compose logs bot --since 1h | grep -iE "whatsapp reminders|recordatorios"
```

---

## 6. Volver a Néctar cuando se resuelva

Solo cuando el equipo de Néctar (o quien revisó el problema) confirme que está resuelto:

1. En el `.env`, poner otra vez `NECTAR_BASE_URL=` con el valor anotado en el paso 2 (está también
   en `.env.antes-de-bird`).
2. Poner otra vez el texto de `NEW_LINE_ANNOUNCEMENT=` (también está en `.env.antes-de-bird`).
3. Reiniciar igual que antes: `docker compose up -d bot` (o el comando de alta carga).
4. Verificar:
   - `curl -s http://localhost:8085/health` → `nectar_host` muestra el host de Néctar (no `"none"`).
   - `docker compose logs bot --since 5m | grep -i "ctar activo"` → aparece «Néctar activo».
   - Que NO aparezca «el desvío a Néctar queda APAGADO»: eso significa que falta alguna clave en el
     `.env` (comparar con `.env.antes-de-bird`).
5. Las conversaciones que se abrieron por Bird mientras tanto siguen en Bird hasta que terminen; las
   nuevas de la línea nueva vuelven a pasar por Néctar.
