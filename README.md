# neuro-bot — WhatsApp Chatbot de Agendamiento Médico

> Bot conversacional para **[Neuroelectrodx](https://neuroelectrodx.com/)**, construido en **Go 1.25**, que permite a los pacientes agendar, consultar y confirmar citas médicas a través de **WhatsApp** (usando la API de **Bird**).

---

## 📋 Tabla de contenido

- [¿Qué hace este bot?](#-qué-hace-este-bot)
- [Arquitectura general](#-arquitectura-general)
- [Estructura de carpetas](#-estructura-de-carpetas)
- [Flujos del bot](#-flujos-del-bot)
  - [Flujo 1 — Saludo e Identificación](#flujo-1--saludo-e-identificación)
  - [Flujo 2 — Registro de Paciente Nuevo](#flujo-2--registro-de-paciente-nuevo)
  - [Flujo 3 — Gestión de Entidad](#flujo-3--gestión-de-entidad)
  - [Flujo 4 — Orden Médica y OCR](#flujo-4--orden-médica-y-ocr)
  - [Flujo 5 — Validaciones Médicas](#flujo-5--validaciones-médicas)
  - [Flujo 6 — Búsqueda y Agendamiento de Citas](#flujo-6--búsqueda-y-agendamiento-de-citas)
  - [Flujo 7 — Consulta, Confirmación y Cancelación de Citas](#flujo-7--consulta-confirmación-y-cancelación-de-citas)
  - [Flujo 8 — Lista de Espera](#flujo-8--lista-de-espera)
  - [Flujo 9 — Escalación a Agente Humano](#flujo-9--escalación-a-agente-humano)
- [Tareas Programadas (Scheduler)](#-tareas-programadas-scheduler)
- [Notificaciones Proactivas](#-notificaciones-proactivas)
- [Base de Datos](#-base-de-datos)
- [Variables de Entorno](#-variables-de-entorno)
- [Cómo ejecutar](#-cómo-ejecutar)
- [Glosario de términos](#-glosario-de-términos)

---

## 🤖 ¿Qué hace este bot?

El bot recibe mensajes de WhatsApp de pacientes y los guía por conversaciones estructuradas para:

| Acción | Descripción |
|--------|-------------|
| **Agendar una cita** | El paciente sube su orden médica (o la escribe), el bot lee los procedimientos (CUPS) con OCR, valida condiciones clínicas y muestra horarios disponibles |
| **Consultar citas** | Muestra las citas pendientes/confirmadas del paciente con la opción de confirmarlas o cancelarlas |
| **Lista de espera** | Si no hay horarios disponibles, inscribe al paciente para recibir una notificación cuando aparezca disponibilidad |
| **Recordatorios automáticos** | El sistema envía recordatorios por WhatsApp y llamadas de voz (IVR) el día anterior a la cita |
| **Registrar paciente nuevo** | Si el paciente no existe en el sistema, el bot lo registra paso a paso |

---

## 🏗 Arquitectura general

```
WhatsApp (usuario)
       │
       ▼
  Bird V2 API  ──webhook──►  /webhook/bird  (api/webhook_handler.go)
                                    │
                                    ▼
                           Worker Pool (goroutines)
                                    │
                                    ▼
                        State Machine (statemachine/)
                         ┌──────────────────────────┐
                         │  Interceptors             │
                         │  → check keywords         │
                         │  → handle notifications   │
                         ├──────────────────────────┤
                         │  Handler actual           │
                         │  (según estado de sesión) │
                         ├──────────────────────────┤
                         │  Auto-chain               │
                         │  (estados automáticos     │
                         │   se ejecutan en cadena   │
                         │   sin esperar input)      │
                         └──────────────────────────┘
                                    │
                         ┌──────────┴──────────┐
                         ▼                     ▼
                    Services/              Bird Client
                    Repository             (enviar msgs)
                    (BD MySQL)
```

### Componentes clave

| Componente | Descripción |
|-----------|-------------|
| **State Machine** | Motor que decide qué handler ejecutar según el estado actual de la sesión del usuario |
| **Session** | Almacena el estado actual del usuario y los datos de contexto (nombre, documento, procedimiento, etc.) |
| **Bird Client** | Cliente HTTP para la API de Bird (enviar mensajes de texto, listas, botones, templates) |
| **Handlers** | Funciones de lógica de negocio para cada estado de la conversación |
| **Scheduler** | Tareas en background que se ejecutan en horarios fijos (recordatorios, limpieza) |
| **Notification Manager** | Maneja respuestas a mensajes proactivos (recordatorios de citas, lista de espera) |
| **Repository** | Capa de acceso a datos MySQL (DatosIPSNDX + base interna) |

---

## 📁 Estructura de carpetas

```
neuro-bot/
├── cmd/server/          # Punto de entrada principal (main.go)
├── internal/
│   ├── api/             # Handlers HTTP (webhook, API interna, middleware)
│   ├── bird/            # Cliente para la API de Bird (enviar msgs, templates, llamadas)
│   ├── config/          # Carga de variables de entorno
│   ├── database/        # Conexión MySQL y migraciones
│   ├── domain/          # Definición de entidades del negocio
│   │   ├── appointment.go   (cita médica y procedimientos)
│   │   ├── doctor.go        (médico/agenda)
│   │   ├── patient.go       (paciente)
│   │   ├── procedure.go     (CUPS: procedimiento médico)
│   │   ├── slot.go          (horario disponible)
│   │   └── waiting_list.go  (entrada en lista de espera)
│   ├── notifications/   # Gestor de notificaciones proactivas
│   ├── repository/      # Acceso a las dos bases de datos
│   │   ├── siesa/       # BD del sistema clínico SIESA / SQL Server (lectura/escritura)
│   │   └── local/       # BD propia del bot (sesiones, eventos, lista de espera, catálogo CUPS)
│   ├── scheduler/       # Tareas programadas (reminders, list de espera, cleanup)
│   ├── services/        # Lógica de negocio reutilizable
│   │   ├── appointment_service.go  (consultar, crear, confirmar, cancelar citas)
│   │   ├── gfr_service.go          (calculo de tasa glomerular para pacientes renales)
│   │   ├── ocr_service.go          (lectura de orden médica via OCR)
│   │   ├── patient_service.go      (buscar y crear pacientes)
│   │   ├── slot_service.go         (buscar horarios disponibles)
│   │   └── procedure_grouper.go    (agrupar CUPS que requieren agendamiento especial)
│   ├── session/         # Modelo de sesión y gestión de estado
│   ├── statemachine/    # Motor de la máquina de estados
│   │   ├── machine.go       (motor: ejecuta handlers, encadena automáticos)
│   │   ├── states.go        (definición de todos los estados)
│   │   ├── config.go        (validación declarativa de inputs)
│   │   ├── handlers/        (lógica de negocio por flujo)
│   │   │   ├── greeting.go           (bienvenida, horario de atención)
│   │   │   ├── identification.go     (búsqueda de paciente por documento)
│   │   │   ├── registration.go       (registro de paciente nuevo)
│   │   │   ├── entity_management.go  (selección y verificación de entidad/EPS)
│   │   │   ├── medical_order.go      (carga de orden médica)
│   │   │   ├── medical_validation.go (validaciones clínicas especiales)
│   │   │   ├── slots.go              (búsqueda de horarios y agendamiento)
│   │   │   ├── appointments.go       (consulta, confirmación, cancelación)
│   │   │   ├── post_action.go        (menú post-acción, cierre de sesión)
│   │   │   ├── escalation.go         (escalación a agente humano)
│   │   │   └── results_locations.go  (resultados y ubicaciones de sedes)
│   │   └── validators/      (validaciones de texto: documento, email, nombre)
│   ├── tracking/        # Registro de eventos para auditoría
│   ├── utils/           # Utilidades (fechas, teléfonos colombianos)
│   └── worker/          # Pool de goroutines para procesar mensajes concurrentes
├── migrations/          # Scripts SQL de migración (up/down)
├── watcher/             # Health check y auto-restart del proceso
├── docker/              # Dockerfiles del servidor
├── docker-compose.yml   # Configuración para levantar todo junto
├── Makefile             # Comandos de desarrollo (build, test, run, migrate)
└── .env.example         # Plantilla de variables de entorno requeridas
```

---

## 💬 Flujos del bot

La máquina de estados tiene dos tipos de estados:

- **Automático** (🤖): Se ejecuta sin esperar input del usuario. Puede encadenarse con el siguiente automático.
- **Interactivo** (👤): Espera una respuesta del usuario (texto, botón o lista).

---

### Flujo 1 — Saludo e Identificación

> Punto de entrada de toda conversación.

```
Usuario envía mensaje
        │
        ▼
🤖 CHECK_BUSINESS_HOURS ─── Fuera de horario ──► 🤖 OUT_OF_HOURS
        │                                                │
     En horario                              👤 OUT_OF_HOURS_MENU
        │                                  (Resultados / Ubicación)
        ▼                                          │
🤖 GREETING                                        ▼
(Bienvenida + menú)                          🤖 TERMINATED
        │
        ▼
👤 MAIN_MENU
  ├── "Agendar cita"   ──► 👤 ASK_CLIENT_TYPE (Flujo 3)
  ├── "Citas Programadas" ► 👤 ASK_DOCUMENT  (Flujo 7)
  ├── "Consultar Resultados" ► 🤖 SHOW_RESULTS
  └── "Ubicación"      ──► 🤖 SHOW_LOCATIONS
```

**Horario de atención:**
- Lunes a Viernes: 7:00 am – 6:00 pm
- Sábados: 7:00 am – 12:00 pm
- Fuera de horario: solo muestra Resultados y Ubicaciones

---

### Flujo 2 — Registro de Paciente Nuevo

> Se activa cuando un paciente no existe en el sistema y quiere agendar una cita.

```
🤖 PATIENT_LOOKUP → paciente no encontrado
        │
        ▼
👤 REGISTRATION_START
  ├── "No, gracias" ──► 🤖 POST_ACTION_MENU
  └── "Sí, registrarme"
          │
          ▼
👤 REG_DOCUMENT_TYPE    (CC / TI / CE / PA / RC / MS / AS)
👤 REG_FIRST_SURNAME    (texto, solo letras)
👤 REG_SECOND_SURNAME   (opcional, o "NA")
👤 REG_FIRST_NAME       (texto, solo letras)
👤 REG_SECOND_NAME      (opcional, o "NA")
👤 REG_BIRTH_DATE       (formato AAAA-MM-DD)
👤 REG_BIRTH_PLACE      (ciudad de nacimiento)
👤 REG_GENDER           (M / F)
👤 REG_MARITAL_STATUS   (SOLTERO / CASADO / UNION LIBRE / DIVORCIADO / VIUDO)
👤 REG_ADDRESS          (dirección completa)
👤 REG_PHONE            (celular colombiano principal)
👤 REG_PHONE2           (celular secundario opcional)
👤 REG_EMAIL            (email o "NA")
👤 REG_OCCUPATION       (ocupación del paciente)
👤 REG_MUNICIPALITY     (búsqueda fuzzy, selección de lista)
👤 REG_ZONE             (U=Urbana / R=Rural)
👤 REG_CLIENT_TYPE      (PARTICULAR / EPS / SOAT)
👤 REG_USER_TYPE        (CONTRIBUTIVO / SUBSIDIADO / PARTICULAR)
👤 REG_AFFILIATION_TYPE (COTIZANTE / BENEFICIARIO / OTRO)
👤 REG_ENTITY           (búsqueda y selección de entidad/EPS)
          │
          ▼
👤 CONFIRM_REGISTRATION → muestra resumen completo
  ├── "Corregir datos" ──► regresa a REG_DOCUMENT_TYPE
  └── "Confirmar"
          │
          ▼
🤖 CREATE_PATIENT (crea en BD externa DatosIPSNDX)
        │
        ▼
👤 ASK_MEDICAL_ORDER (continúa al Flujo 4)
```

---

### Flujo 3 — Gestión de Entidad

> Se ejecuta cuando el usuario elige "Agendar cita" para determinar su entidad pagadora.

```
👤 ASK_CLIENT_TYPE (7 opciones de entidad)
  ├── PARTICULAR (1) ──► 👤 ASK_DOCUMENT (sin entidad requerida)
  ├── EPS (2)
  ├── PREPAGADA (3)
  ├── REGIMEN ESPECIAL (4)
  ├── SOAT (5)
  ├── ARL (6)
  └── POLIZA (7)
           │
           ▼
🤖 CHECK_ENTITY → verifica si la entidad del paciente ya coincide
   ├── Coincide ──► 🤖 ASK_DOCUMENT (continúa)
   └── No coincide
           │
           ▼
👤 CONFIRM_ENTITY ─── "Sí, continuar" ──► 👤 ASK_DOCUMENT
                  └── "Cambiar entidad"
                               │
                               ▼
👤 CHANGE_ENTITY ──► 👤 SHOW_ENTITY_LIST → selección ──► 👤 ASK_ENTITY_NUMBER ──► 👤 ASK_DOCUMENT
```

---

### Flujo 4 — Orden Médica y OCR

> El paciente sube su orden médica (imagen) o escribe el código CUPS manualmente.

```
👤 ASK_MEDICAL_ORDER
  ├── "Tengo orden médica" ──► 👤 UPLOAD_MEDICAL_ORDER
  │                                    │
  │                             (usuario envía imagen)
  │                                    │
  │                                    ▼
  │                            🤖 VALIDATE_OCR
  │                          (analiza imagen con OCR)
  │                           ├── Éxito ──► 👤 CONFIRM_OCR_RESULT
  │                           │              ├── "Sí, correcto" ──► 🤖 CHECK_SPECIAL_CUPS
  │                           │              └── "No, corregir" ──► 👤 ASK_MANUAL_CUPS
  │                           └── Falló ──► 🤖 OCR_FAILED ──► 👤 ASK_MANUAL_CUPS
  │
  └── "No tengo / Soy particular" ──► 👤 ASK_MANUAL_CUPS
                                            │
                                            ▼
                                    👤 MANUAL_PROCEDURE_INPUT
                                    (escribe código CUPS)
                                            │
                                            ▼
                                    👤 SELECT_PROCEDURE
                                    (selecciona de la lista)
                                            │
                                            ▼
                                    🤖 CHECK_SPECIAL_CUPS (Flujo 5)
```

---

### Flujo 5 — Validaciones Médicas

> Una serie de controles clínicos que pueden requerir datos adicionales antes de buscar horarios.

```
🤖 CHECK_SPECIAL_CUPS → detecta si el procedimiento tiene restricciones especiales
        │
        ▼
🤖 ASK_CONTRASTED → ¿requiere contraste? (auto-omite si no aplica)
        │
        ▼
🤖 ASK_PREGNANCY → ¿está embarazada? (auto-omite para hombres y bebés)
  ├── Sí embarazada con ciertos procedimientos ──► 🤖 PREGNANCY_BLOCK (rechaza el agendamiento)
  └── Aplica ──► 👤 ASK_BABY_WEIGHT (peso del bebé en categorías)
        │
        ▼
🤖 CHECK_EXISTING → ¿ya tiene cita para este procedimiento?
  └── Sí ──► 🤖 APPOINTMENT_EXISTS (informa que ya existe, pregunta si continúa)
        │
        ▼
🤖 CHECK_PRIOR_CONSULTATION → ¿necesita consulta previa?
        │
        ▼
🤖 CHECK_SOAT_LIMIT → ¿el SOAT tiene límite de atenciones?
        │
        ▼
🤖 CHECK_AGE_RESTRICTION → ¿hay restricción de edad para este procedimiento?
        │
        ▼
── Para procedimientos renales (TFG/GFR) ──►
   👤 GFR_CREATININE → valor de creatinina sérica
   👤 GFR_HEIGHT     → estatura cm
   👤 GFR_WEIGHT     → peso kg
   👤 GFR_DISEASE    → tipo de enfermedad renal
   🤖 GFR_RESULT     → calcula tasa glomerular
     ├── No elegible ──► 🤖 GFR_NOT_ELIGIBLE (rechaza agendamiento)
     └── Elegible ──► continúa
        │
        ▼
🤖 ASK_SEDATION → ¿requiere sedación? (auto-omite si no aplica)
        │
        ▼
🤖 SEARCH_SLOTS (Flujo 6)
```

---

### Flujo 6 — Búsqueda y Agendamiento de Citas

> Muestra horarios disponibles y permite al paciente confirmar la cita.

```
🤖 SEARCH_SLOTS → consulta API externa con filtros de edad, contraste, sedación, espacios
  ├── Sin horarios ──► 🤖 NO_SLOTS_AVAILABLE
  │                           ├── Reprogramación por cancelación admin ──► auto-inscribe en lista de espera
  │                           └── Normal ──► 👤 OFFER_WAITING_LIST (Flujo 8)
  │
  └── Con horarios (máx 5)
           │
           ▼
👤 SHOW_SLOTS → lista interactiva con fecha/hora/doctor
  ├── "Ver más horarios" ──► 🤖 SEARCH_SLOTS (siguiente página)
  └── Selecciona horario
           │
           ▼
👤 CONFIRM_BOOKING → resumen de la cita
  ├── "Elegir otro" ──► 🤖 SEARCH_SLOTS
  └── "Confirmar cita"
           │
           ▼
🤖 CREATE_APPOINTMENT → crea en BD externa (DatosIPSNDX)
  ├── Error "slot tomado" ──► 🤖 BOOKING_FAILED ──► 🤖 SEARCH_SLOTS (busca nuevos)
  ├── Error general ──► 🤖 BOOKING_FAILED ──► 👤 POST_ACTION_MENU
  └── Éxito
           │
           ▼
🤖 BOOKING_SUCCESS
  ├── Muestra confirmación + instrucciones de preparación del procedimiento
  ├── Si es reprogramación ──► cancela cita anterior
  ├── Si hay más procedimientos ──► repite Flujo 5 para el siguiente
  └── Flujo completo ──► 👤 POST_ACTION_MENU
```

---

### Flujo 7 — Consulta, Confirmación y Cancelación de Citas

> El paciente consulta sus citas para confirmar o cancelar.

```
👤 ASK_DOCUMENT → ingresa número de cédula
        │
        ▼
🤖 PATIENT_LOOKUP → busca en BD
        │
        ▼
👤 CONFIRM_IDENTITY → "¿Eres tú?"
  └── "Sí, soy yo"
           │
           ▼
🤖 FETCH_APPOINTMENTS → consulta citas pendientes/confirmadas
  ├── Sin citas ──► "No tienes citas pendientes" ──► 👤 POST_ACTION_MENU
  └── Con citas
           │
           ▼
👤 LIST_APPOINTMENTS → lista interactiva (agrupa bloques consecutivos)
  └── Selecciona cita
           │
           ▼
👤 APPOINTMENT_ACTION → muestra detalle de la cita
  ├── "Volver" ──► 👤 LIST_APPOINTMENTS
  ├── "Confirmar" ──► confirma bloque completo en BD ──► 👤 POST_ACTION_MENU
  └── "Cancelar cita" ──► cancela bloque en BD
                                │
                                ▼
              🤖 Notifica lista de espera si el CUPS quedó libre
                                │
                                ▼
                         👤 POST_ACTION_MENU
```

> **Bloques consecutivos**: Si un paciente tiene varias citas encadenadas para el mismo procedimiento (ej.: exámenes que necesitan múltiples espacios), se muestran y gestionan como un bloque unificado.

---

### Flujo 8 — Lista de Espera

> Cuando no hay horarios disponibles, el paciente puede inscribirse para recibir una notificación futura.

```
🤖 NO_SLOTS_AVAILABLE
        │
        ▼
👤 OFFER_WAITING_LIST
  ├── "No, gracias" ──► 👤 POST_ACTION_MENU
  └── "Sí, avisarme"
           │
           ▼
  Guarda en BD (tabla waiting_list):
  - ID paciente, teléfono, CUPS, nombre del procedimiento
  - Datos clínicos (contraste, sedación, embarazo, GFR si aplica)
  - Válido por 30 días
           │
           ▼
  Confirmación: "Te avisaremos por WhatsApp cuando haya disponibilidad"
           │
           ▼
       👤 POST_ACTION_MENU
```

**¿Cómo se procesan las notificaciones de lista de espera?**

1. **08:00 diario (lunes a viernes)**: El scheduler busca CUPS con entradas en espera, verifica si hay horarios disponibles y envía una plantilla de WhatsApp a los primeros pacientes en la cola (FIFO = primero en entrar, primero en salir).
2. **Tiempo real**: Cuando un paciente cancela una cita, el sistema inmediatamente verifica si hay alguien en la lista de espera para ese CUPS y le notifica.
3. **Respuesta del paciente**: El paciente recibe un mensaje y tiene 6 horas para responder. Si acepta, se inicia automáticamente el flujo de agendamiento.

---

### Flujo 9 — Escalación a Agente Humano

> Se activa cuando el bot no puede resolver el problema del paciente.

```
Síntomas que disparan escalación:
  ├── El paciente falla 3 veces consecutivas en responder correctamente
  ├── El paciente escribe "agente", "humano", "asesor", etc.
  └── Error crítico en proceso de agendamiento

        │
        ▼
🤖 ESCALATE_TO_AGENT
  → Notifica a través de Bird Inbox para que un humano tome la conversación
  → Registra el evento con el estado de la sesión
        │
        ▼
🤖 ESCALATED (sesión queda en estado escalado)
```

---

## ⏰ Tareas Programadas (Scheduler)

El scheduler se ejecuta en segundo plano y corre automáticamente en los horarios indicados:

| Hora | Días | Tarea | Descripción |
|------|------|-------|-------------|
| **02:00** | Todos | `data_cleanup` | Expira entradas de lista de espera mayores a 30 días |
| **07:00** | L–S | `whatsapp_reminders` | Envía mensaje de recordatorio de WhatsApp a los pacientes con cita al día siguiente |
| **08:00** | L–V | `waiting_list_check` | Verifica disponibilidad de horarios para pacientes en lista de espera y les notifica |
| **15:00** | L–S | `voice_reminders` | Realiza llamadas IVR a los pacientes que no respondieron el recordatorio de WhatsApp |

---

## 📲 Notificaciones Proactivas

El bot no solo responde, también **inicia conversaciones** con los pacientes mediante plantillas de WhatsApp. El `NotificationManager` maneja las respuestas a estos mensajes:

| Tipo | Cuándo se envía | Opciones del paciente | Tiempo de espera |
|------|-----------------|-----------------------|-----------------|
| `confirmation` | Recordatorio de cita del día siguiente | Confirmar / Cancelar | 6 horas |
| `reschedule` | Si la cita fue cancelada administrativamente | Agendar nueva / Ignorar | 6 horas |
| `waiting_list` | Cuando hay disponibilidad para su CUPS | Agendar ahora / No gracias | 6 horas |

Si el paciente **no responde en 6 horas**:
- `confirmation`: si era primer intento, se envía llamada IVR; si era segundo intento, se cierra la notificación.
- `waiting_list`: se marca la entrada como expirada y se pasa al siguiente en la cola.
- Las notificaciones se **persisten en BD** para sobrevivir reinicios del servidor.

---

## 🗄 Base de Datos

El bot usa **dos bases de datos MySQL**:

### 1. DatosIPSNDX (base clínica externa — lectura/escritura selectiva)
Contiene los datos del sistema clínico existente: pacientes, citas, médicos, agendas, CUPS.

Tablas principales: `pacientes`, `citas`, `detalle_citas`, `agendas`, `cups_procedimientos`, `entidades`, `municipios`

### 2. Base de datos local del bot (neuro-bot)

| Tabla | Descripción |
|-------|-------------|
| `sessions` | Estado actual de la conversación de cada usuario (número, estado, contextos) |
| `session_context` | Pares clave-valor con datos de la conversación (documento, nombre, procedimiento seleccionado, etc.) |
| `chat_events` | Log de eventos para auditoría (estado anterior → siguiente, datos del evento) |
| `communication_messages` | Registro de mensajes enviados/recibidos |
| `communication_calls` | Registro de llamadas IVR realizadas |
| `waiting_list` | Entradas de pacientes en lista de espera con todos sus datos clínicos |
| `center_locations` | Sedes físicas del centro médico (nombre, dirección, mapa, teléfono) |
| `notification_pending` | Notificaciones proactivas pendientes de respuesta (persiste en restart) |

---

## 🔧 Variables de Entorno

Copia `.env.example` a `.env` y completa los valores. Las más importantes:

```env
# === Servidor ===
PORT=8080
ENVIRONMENT=development        # o "production"

# === Base de datos clínica (lectura) ===
DB_DATASIPSNDX_DSN=usuario:contraseña@tcp(host:puerto)/nombre_bd

# === Base de datos local del bot ===
DB_LOCAL_DSN=usuario:contraseña@tcp(host:puerto)/nombre_bd

# === API de Bird (WhatsApp) ===
BIRD_API_KEY=sk-...
BIRD_WORKSPACE_ID=...
BIRD_CHANNEL_ID=...

# === Plantillas de WhatsApp ===
BIRD_TEMPLATE_CONFIRM_PROJECT_ID=...   # Template de recordatorio de cita
BIRD_TEMPLATE_CONFIRM_VERSION_ID=...
BIRD_TEMPLATE_CONFIRM_LOCALE=es

BIRD_TEMPLATE_WL_PROJECT_ID=...        # Template de disponibilidad lista de espera
BIRD_TEMPLATE_WL_VERSION_ID=...

# === Bot ===
BOT_NAME=NeuroBot                     # Nombre del asistente virtual
CENTER_NAME=Neuroelectrodx            # Nombre del centro médico
RESULTS_URL=https://neuroelectrodx.com/

# === OCR ===
OCR_API_URL=...                        # URL del servicio OCR para leer órdenes médicas
OCR_API_KEY=...

# === Tests ===
TESTING_ALWAYS_OPEN=false              # true para omitir validación de horario en desarrollo
```

---

## 🚀 Cómo ejecutar

### Con Docker (recomendado)

```bash
# Copiar variables de entorno
cp .env.example .env
# Editar .env con los valores reales

# Levantar todo (bot + MySQL)
docker compose up -d
```

### Desarrollo local

```bash
# Ejecutar migraciones
make migrate-up

# Correr el servidor
make run

# Correr tests
make test

# Compilar
make build
```

---

## 📖 Glosario de términos

| Término | Significado |
|---------|-------------|
| **CUPS** | Clasificación Única de Procedimientos en Salud — código estándar colombiano para procedimientos médicos (ej.: `890302` = Electroencefalograma) |
| **Entidad** | EPS, prepagada, SOAT, ARL u otro ente pagador de la atención médica |
| **Slot** | Un horario disponible específico con fecha, hora y médico asignado |
| **Bloque consecutivo** | Varias citas seguidas del mismo paciente para el mismo procedimiento (cuando el procedimiento requiere más de un espacio de 30 min) |
| **OCR** | Reconocimiento óptico de caracteres — el bot lee la imagen de la orden médica y extrae los códigos CUPS automáticamente |
| **TFG / GFR** | Tasa de Filtración Glomerular — cálculo requerido para pacientes con enfermedad renal antes de autorizar ciertos procedimientos |
| **Bird** | Plataforma de mensajería (ex-MessageBird) usada como canal de WhatsApp Business API |
| **IVR** | Respuesta de Voz Interactiva — llamadas automáticas de recordatorio |
| **DatosIPSNDX** | Nombre del sistema de información clínica existente al que el bot se conecta |
| **Lista de espera** | Cola de pacientes que quieren agendar un procedimiento sin horarios disponibles actualmente |
| **Sesión** | Registro en base de datos del estado actual de la conversación de un usuario. Expira tras 30 minutos de inactividad |

---

*Documentación generada el 7 de marzo de 2026 con base en el análisis completo del código fuente.*
