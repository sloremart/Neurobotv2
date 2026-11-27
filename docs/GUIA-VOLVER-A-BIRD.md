# Guía: volver a Bird

> **Tras el traslado del número viejo a Néctar (línea vieja, `NECTAR_LEGACY_*`) esta guía NO aplica.**
> Ese número ya no está en Bird: vaciar `NECTAR_BASE_URL` no lo devuelve y dejaría esas conversaciones
> sin atención. La contingencia es que los supervisores de Néctar rescaten las conversaciones del bot
> («escalar», motivo `bot_down`). Con la línea vieja armada, el aviso URGENTE ya lo dice así.

Para la persona de turno que recibe en Telegram el aviso **«🚨 URGENTE — volver a Bird»**. No hace
falta saber programar: son cambios en el `.env` del servidor, un reinicio del bot y un rescate de
conversaciones en Néctar.

Mientras dura la transición el bot atiende dos líneas: la de siempre (Bird) y la nueva (Néctar). Los
recordatorios y avisos salen por la nueva. Si la nueva falla, esta guía devuelve TODO a Bird.

> **Requisito previo (antes del arranque de la línea nueva):** la escalación debe estar habilitada
> para la entidad de Neuro en Néctar (`escalation_enabled`). Sin ella, el rescate del paso 4 falla
> (Néctar le responde 403 al supervisor) y los pacientes que estaban con el bot en la línea nueva
> quedan sin quién los atienda.

---

## 1. Antes de cambiar nada: ¿es una falsa alarma? (2-5 minutos)

El aviso dice el **disparador**, un **detalle** (cuántos fallos y desde qué hora) y la **hora**.

| Disparador (lo que dice el aviso) | Qué significa | ¿Puede ser falsa alarma? |
|---|---|---|
| **Plantillas proactivas detenidas por configuración** (`proactive_stopped`) | Néctar rechazó una plantilla por algo de la cuenta (clave, plantilla, canal). La corrida se detuvo: no salió nada más en esa corrida. | **No. Tratarlo como real.** |
| **Recordatorios sin enviar por encima del umbral** (`reminders_unsent`) | Una corrida de recordatorios dejó muchos (por defecto 10 o más) sin enviar o «en duda» por fallas de la línea. No cuentan los números sin WhatsApp ni los pacientes que ya hablan con un agente. | **No. Tratarlo como real.** |
| **Néctar no responde** (`nectar_down`) | En pocos minutos (por defecto 5 fallos dentro de 5 min) los envíos o lecturas a Néctar fallaron por red, tiempo vencido o error del servidor de Néctar (5xx). No cuentan el último reintento de un mensaje ni un envío que el propio bot abandonó porque se le acabó el plazo del turno. | Puede ser un corte corto. Ver abajo. |
| **Escalaciones a Néctar fallando** (`escalation_failing`) | Al pasar pacientes a un agente de Néctar, Néctar los rechazó varias veces (por defecto 3 dentro de 10 min) por algo que no es una caída (permiso, clave, escalación apagada). | Puede ser algo puntual. Ver abajo. |

**Los dos primeros no se repiten solos.** Avisan una vez por corrida (recordatorios del día antes a
las 07:00, los del mismo día cada hora, lista de espera a las 06:00, avisos administrativos cuando
alguien los lanza). Que no llegue otro aviso NO significa que se arregló: la próxima corrida puede
estar a horas. Cambiar a Bird.

**Para «Néctar no responde» y «Escalaciones a Néctar fallando»**, comprobar:

```bash
# En el servidor, en la carpeta del proyecto (8085 es el PORT del .env de producción; si es otro, usar ese)
curl -s http://localhost:8085/health

# Errores y avisos urgentes de los últimos 15 minutos (los logs son JSON)
docker compose logs bot --since 15m | grep -i '"level":"ERROR"\|aviso urgente'
```

Si ese comando no muestra nada, eso NO prueba que Néctar esté bien: algunos fallos se registran como
advertencia y no salen ahí. Hacer igual las comprobaciones de abajo.

- Abrir el panel web de Néctar: ¿carga?, ¿los agentes ven y contestan conversaciones? Si NO carga,
  es real.
- Preguntar a un agente de Néctar si le están llegando pacientes del bot.
- Si en los logs dejaron de aparecer errores nuevos de Néctar **mientras había pacientes escribiendo o
  escalando** (hay tráfico), el corte probablemente pasó. Sin tráfico (de noche, domingo) que no haya
  errores nuevos no prueba nada: nadie intentó usar Néctar.
- Si `/health` dice `"status":"critical"` o `"local_db":"error"`, además el bot tiene otro problema:
  avisar al equipo técnico (volver a Bird no arregla eso).

**Si hay duda, cambiar a Bird.** Volver a Néctar después es igual de fácil (paso 7).

---

## 2. Cambiar a Bird

1. Entrar al servidor y a la carpeta del proyecto (la misma donde se hace `git pull`).
2. Hacer una copia del `.env` y protegerla (tiene claves secretas):
   ```bash
   cp .env .env.antes-de-bird
   chmod 600 .env.antes-de-bird
   ```
3. Abrir el `.env` (`nano .env`) y cambiar SOLO esto:
   - `NECTAR_BASE_URL=` → dejarla **vacía** (el valor que tenía queda en `.env.antes-de-bird`).
   - `NEW_LINE_ANNOUNCEMENT=` → recomendado dejarla **vacía**, pero no es obligatorio: sin
     `NECTAR_BASE_URL` el bot ya no anuncia la línea nueva aunque el texto siga puesto.
   - Las demás variables `NECTAR_*` se dejan como están: sin `NECTAR_BASE_URL` no se usan, y así
     volver es solo poner la URL otra vez.
   - **Se pierden los festivos de Néctar:** sin `NECTAR_BASE_URL` no se lee nada de Néctar, así que rige el
     horario de siempre sin festivos: **el bot abre en festivo**, y las escalaciones de ese día se
     desvían por «no hay asesores» (la disponibilidad de Bird), no por horario. Si el cambio cae cerca de
     un festivo, avisar al call center.
4. Guardar y reiniciar el bot para que lea el `.env` nuevo:
   ```bash
   docker compose up -d bot
   ```
   > **Ojo:** `docker compose restart bot` NO sirve aquí: reinicia con las variables viejas.
   > Si el servidor corre en el perfil de alta carga (`./scripts/scale-up.sh`), usar:
   > `docker compose -f docker-compose.yml -f docker-compose.high-load.yml up -d bot`

---

## 3. Verificar que quedó en Bird

Justo después de reiniciar:

```bash
docker compose ps                 # neuro_bot debe quedar "healthy" (tarda unos segundos)
curl -s http://localhost:8085/health
docker compose logs bot --since 30m | grep -i "ctar activo"
```

- En `/health`, `nectar_host` debe decir **`"none"`**.
- La búsqueda de «Néctar activo» solo puede mostrar líneas de ANTES del reinicio; si aparece una con
  la hora del reinicio o posterior, el cambio no tomó (revisar el `.env` y repetir el paso 2.4).
- Desde ese momento no llegan más avisos urgentes: solo existen con la línea nueva encendida.

---

## 4. Rescatar en Néctar las conversaciones que tenía el bot

Con la línea nueva apagada, el bot **no atiende nada de Néctar**: lo que llegue desde Néctar se
recibe y se descarta (a Néctar le contesta «ok», así que la alerta propia de Néctar de bot caído
**no salta**). Los pacientes que estaban conversando con el bot en la línea nueva quedan sin respuesta
hasta que alguien los rescate.

Los agentes de Néctar **ven** esas conversaciones, pero un agente normal **no puede contestarlas**.
Un **supervisor o administrador de Néctar** debe rescatarlas, antes o justo después del cambio:

1. Abrir cada conversación que sigue abierta con el bot.
2. Pulsar **«escalar»** con el motivo **`bot_down`** (el bot no responde). También sirve
   `manual_rescue`.
3. La conversación pasa a la cola y un agente la atiende normalmente.

Es **una por una**: no hay rescate masivo. Si Néctar responde 403 al escalar, la escalación no está
habilitada para la entidad de Neuro (ver el requisito previo al inicio): pedir que la activen.

Texto sugerido para que el agente le escriba al paciente rescatado:

> Hola, tuvimos un inconveniente técnico con el asistente automático de esta línea. Ya te atiende
> una persona del equipo. Si prefieres, también puedes escribirnos a nuestra línea de WhatsApp de
> siempre, donde te atendemos con normalidad para agendar, confirmar o reprogramar tu cita.

---

## 5. Qué pasa con lo que el bot tenía pendiente de la línea nueva

- **El bot no le escribe nada a esas conversaciones ni toca Bird por ellas**: cualquier envío,
  cierre o escalación hacia una conversación de Néctar se corta antes de salir, sin llamar a Bird.
- **Recordatorios de mañana que ya salieron por la línea nueva y el paciente no ha contestado:**
  siguen yendo a la **llamada automática (IVR) de las 13:00** (si el IVR está encendido,
  `IVR_NOTIFICATIONS_ENABLED=true`, como en producción). La llamada sale por Bird y marca al teléfono
  del paciente; el paciente oprime 1 para confirmar o cualquier otra tecla para cancelar. No hay que
  hacer nada.
- Si el paciente oprime un botón del recordatorio en la línea nueva DESPUÉS del cambio, el bot no lo
  registra (se descarta): queda para la llamada de las 13:00 si el cambio fue antes de esa hora, o
  para el call center.
- Los recordatorios del **mismo día** (los que salen cada hora) no tienen llamada: los que quedaron
  sin respuesta no se recuperan solos.
- **Mensajes de Néctar a medio procesar:** si al reiniciar quedaban mensajes de Néctar recibidos
  pero sin terminar de procesar, el bot **ya no los procesa** (ni al arrancar ni después): los da
  por cerrados sin tocar las citas, igual que lo que llegue de Néctar mientras esté en Bird. Un
  «Confirmar» que quedó así no confirma la cita; esa cita sigue el camino de arriba (llamada de las
  13:00 o call center).

---

## 6. Recordatorios y avisos desde ahora

Con la línea nueva apagada, los recordatorios del día antes (07:00), los del mismo día, la lista de
espera y los avisos administrativos salen **por las plantillas de Bird de siempre**, sin tocar nada
más.

Si el aviso fue en plena corrida de recordatorios, los pacientes que quedaron sin recordatorio
**no se vuelven a enviar por WhatsApp**: el bot los dejó apuntados para la llamada de las 13:00 (con
el IVR encendido). No hace falta relanzar la corrida.

Para revisar la siguiente corrida (por ejemplo, la de las 07:00 del día siguiente):

```bash
docker compose logs bot --since 1h | grep -iE "whatsapp reminders|recordatorios"
```

---

## 7. Volver a Néctar cuando se resuelva

Solo cuando el equipo de Néctar (o quien revisó el problema) confirme que está resuelto:

1. En el `.env`, poner otra vez `NECTAR_BASE_URL=` y el texto de `NEW_LINE_ANNOUNCEMENT=` con los
   valores de `.env.antes-de-bird`.
2. Reiniciar igual que antes: `docker compose up -d bot` (o el comando de alta carga).
3. Verificar, justo después de reiniciar:
   - `curl -s http://localhost:8085/health` → `nectar_host` muestra el host de Néctar (no `"none"`).
   - `docker compose logs bot --since 30m | grep -i "ctar activo"` → aparece «Néctar activo» con la
     hora del reinicio.
   - Que NO aparezca «el desvío a Néctar queda APAGADO»: eso significa que falta alguna clave en el
     `.env` (comparar con `.env.antes-de-bird`).
4. Borrar la copia, que tiene claves secretas: `rm .env.antes-de-bird` (o guardarla protegida si es
   la única copia de esos valores).
5. Las conversaciones que se abrieron por Bird mientras tanto siguen en Bird hasta que terminen; las
   nuevas de la línea nueva vuelven a pasar por Néctar. Lo que Néctar mandó al bot mientras estuvo
   apagado se descartó: no llega atrasado.
