# 🛠 Guía de Edición del Proyecto neuro-bot

> Esta guía explica **exactamente qué archivos crear o modificar** para cualquier tipo de cambio en el bot.
> Léela completamente antes de hacer cualquier modificación. Ningún cambio en el bot queda en un solo archivo.

---

## ⚠️ Regla de entorno: SIEMPRE ejecutar en modo pruebas

**Todo desarrollo y prueba debe ejecutarse con `APP_ENV=testing`** (canal de pruebas: +57 8 2795066).

**Nunca ejecutar el bot en producción desde tu máquina local.** Una vez que el bot esté en producción, solo correrá en el servidor final. Si se ejecuta desde otro equipo con `APP_ENV=production`, el túnel ngrok de producción (`app.colibrixa.com`) se cae porque ngrok solo permite una conexión por hostname — el servidor real pierde su conexión y el bot de producción deja de funcionar.

```env
# .env — SIEMPRE debe tener esto durante desarrollo:
APP_ENV=testing
```

> En el servidor de producción, `APP_ENV` estará en `production` y apuntará al hostname ngrok fijo. Localmente, `.env.testing` sobreescribe con el hostname de pruebas (`endodermal-prearticulate-teodora.ngrok-free.dev`), que es seguro de usar simultáneamente.

---

## 📌 Principios fundamentales

Antes de editar, entiende estas reglas del proyecto:

1. **Cada estado de la conversación tiene su handler** — No existe lógica de conversación fuera de los handlers en `internal/statemachine/handlers/`.
2. **Todo estado debe declararse en `states.go`** — Si un estado no está en `states.go`, no existe para la máquina.
3. **Todo estado debe tener una entrada en `stateTypes` (en `states.go`)** — Debe decir si es `Automatic` (🤖) o `Interactive` (👤).
4. **Todo estado debe tener su handler registrado** — Sin registro en `machine.go` (o el archivo de configuración del servidor), el estado tendrá error en runtime.
5. **Los mensajes van en los handlers, no en variables globales** — El texto que ve el usuario se escribe directamente dentro del handler.
6. **Los datos de la conversación viajan en el "contexto de sesión"** — Para pasar datos entre estados usa `WithContext("clave", valor)` al salir y `sess.GetContext("clave")` al entrar.

---

## 📋 Índice de escenarios

| Tipo de cambio | Sección |
|----------------|---------|
| Cambiar el texto de un mensaje | [→](#1--cambiar-el-texto-de-un-mensaje) |
| Cambiar las opciones de un botón o lista | [→](#2--cambiar-las-opciones-de-un-botón-o-lista) |
| Agregar un paso extra en un flujo existente | [→](#3--agregar-un-paso-extra-en-un-flujo-existente) |
| Crear un flujo nuevo completo | [→](#4--crear-un-flujo-nuevo-completo) |
| Eliminar un paso de un flujo | [→](#5--eliminar-un-paso-de-un-flujo) |
| Agregar una validación de texto nueva | [→](#6--agregar-una-validación-de-texto-nueva) |
| Cambiar el horario de atención | [→](#7--cambiar-el-horario-de-atención) |
| Agregar un nuevo campo en el registro de paciente | [→](#8--agregar-un-nuevo-campo-en-el-registro-de-paciente) |
| Cambiar la lógica del scheduler (tareas automáticas) | [→](#9--cambiar-la-lógica-del-scheduler-tareas-automáticas) |
| Agregar una notificación proactiva nueva | [→](#10--agregar-una-notificación-proactiva-nueva) |
| Agregar una variable de entorno nueva | [→](#11--agregar-una-variable-de-entorno-nueva) |
| Agregar una nueva tabla en la base de datos | [→](#12--agregar-una-nueva-tabla-en-la-base-de-datos) |
| Modificar acceso a datos (repository) | [→](#13--modificar-acceso-a-datos-repository) |
| Agregar un interceptor global (keyword especial) | [→](#14--agregar-un-interceptor-global-keyword-especial) |
| Cambiar cuántos reintentos tiene el bot antes de escalar | [→](#15--cambiar-cuántos-reintentos-antes-de-escalar-a-agente) |
| Agregar/quitar una keyword de menú o escalación | [→](#16--agregar-o-quitar-una-keyword-de-menú-o-escalación) |
| Agregar tipo de mensaje no soportado | [→](#17--agregar-un-tipo-de-mensaje-no-soportado) |
| Modificar campos directos de la Sesión | [→](#18--modificar-campos-directos-del-modelo-de-sesión) |
| Agregar un comando de agente (/bot) | [→](#19--agregar-un-comando-de-agente-bot) |
| Cambiar el timeout de inactividad de sesión | [→](#20--cambiar-el-timeout-de-inactividad-de-sesión) |
| Agregar evento de auditoría (tracking) | [→](#21--agregar-evento-de-auditoría-tracking) |
| Cambiar la lógica de agrupación de procedimientos | [→](#22--cambiar-la-lógica-de-agrupación-de-procedimientos) |
| Agregar un nuevo endpoint HTTP | [→](#23--agregar-un-nuevo-endpoint-http) |
| Cambiar el mensaje de tipo de input no esperado | [→](#24--cambiar-el-mensaje-cuando-el-usuario-envía-un-tipo-de-input-inesperado) |
| Cambiar concurrencia del worker pool | [→](#25--cambiar-la-concurrencia-del-worker-pool) |
| Agregar o modificar un feature flag | [→](#26--agregar-o-modificar-un-feature-flag) |
| Modificar los límites mensuales CUPS (SOAT) | [→](#27--modificar-los-límites-mensuales-cups-grupos-soat) |
| Modificar el enrutamiento de equipos por especialidad | [→](#28--modificar-el-enrutamiento-de-equipos-por-especialidad) |
| Modificar el flujo de escalación de confirmación | [→](#29--modificar-el-flujo-de-escalación-de-confirmación) |
| Modificar el flujo de reprogramación por notificación | [→](#30--modificar-el-flujo-de-reprogramación-por-notificación) |
| Modificar el flujo de lista de espera | [→](#31--modificar-el-flujo-de-lista-de-espera) |
| Agregar o modificar un template de Bird (WhatsApp) | [→](#32--agregar-o-modificar-un-template-de-bird-whatsapp) |
| Configuración multi-ambiente (.env / .env.testing) | [→](#33--configuración-multi-ambiente-env--envtesting) |
| Referencia de claves del contexto de sesión | [→](#34--referencia-de-claves-del-contexto-de-sesión) |
| Patrones de testing y mocks | [→](#35--patrones-de-testing-y-mocks) |
| Configuración Docker | [→](#36--configuración-docker) |

---

## 1 — Cambiar el texto de un mensaje

**Caso:** Quieres cambiar lo que dice el bot en algún paso.

### Archivos a modificar

| Archivo | Qué cambiar |
|---------|-------------|
| `internal/statemachine/handlers/<flujo>.go` | Busca el handler del estado correspondiente y edita el string del mensaje |

### Cómo identificar el handler correcto

1. Identifica en qué estado del flujo aparece el mensaje.
2. Busca el nombre del estado en `internal/statemachine/states.go` (ej: `StateGreeting`).
3. Abre el handler file correspondiente:

| Flujo | Archivo |
|-------|---------|
| Saludo, menú principal, horario | `handlers/greeting.go` |
| Identificación por documento | `handlers/identification.go` |
| Registro de paciente nuevo | `handlers/registration.go` |
| Entidad/EPS | `handlers/entity_management.go` |
| Orden médica, OCR | `handlers/medical_order.go` |
| Validaciones clínicas | `handlers/medical_validation.go` |
| Horarios y agendamiento | `handlers/slots.go` |
| Consultar/confirmar/cancelar citas | `handlers/appointments.go` |
| Menú post-acción, despedida | `handlers/post_action.go` |
| Escalación a agente | `handlers/escalation.go` |
| Resultados y ubicaciones | `handlers/results_locations.go` |

### Ejemplo

```go
// ANTES
WithText("Por favor ingresa tu número de documento:")

// DESPUÉS
WithText("Ingresa tu cédula o documento de identidad (sin puntos ni espacios):")
```

### También revisar

- Si el mismo mensaje también aparece en el `RetryPrompt` del `RegisterWithConfig`, actualízalo ahí también.
- Busca con `grep -r "texto antiguo"` en `internal/statemachine/handlers/` para no perderte ninguna copia.

---

## 2 — Cambiar las opciones de un botón o lista

**Caso:** Quieres agregar, quitar o renombrar una opción del menú.

### Archivos a modificar

| Archivo | Qué cambiar |
|---------|-------------|
| `internal/statemachine/handlers/<flujo>.go` | Modifica el array `Options` y los `ListRow`/`Button` del estado |
| `internal/statemachine/handlers/<flujo>.go` | Modifica el `switch selected` dentro del handler para manejar la nueva opción |

### Pasos obligatorios

1. **Agregar a `Options`**: En el `RegisterWithConfig`, el campo `Options: []string{...}` define qué payloads son válidos. Debes incluir el nuevo payload aquí o el bot lo rechazará como `input inválido`.
2. **Agregar al `switch`**: El handler de negocio hace `switch selected { case "opcion": ... }`. Agrega tu nuevo `case`.
3. **Agregar a la UI**: En el `RetryPrompt` y en el handler anterior (que muestra los botones por primera vez), agrega el botón/fila correspondiente.

### Ejemplo: agregar opción "Cambiar paciente" al menú principal

```go
// 1. En Options del RegisterWithConfig:
Options: []string{"consultar", "agendar", "resultados", "ubicacion", "cambiar_paciente"},

// 2. En el switch del handler:
case "cambiar_paciente":
    return sm.NewResult(sm.StateChangePatient).
        WithEvent("menu_selected", map[string]interface{}{"option": "cambiar_paciente"}), nil

// 3. En el RetryPrompt y en greetingHandler, agrega la fila:
{ID: "cambiar_paciente", Title: "Cambiar paciente", Description: "Cambiar el paciente actual"},
```

---

## 3 — Agregar un paso extra en un flujo existente

**Caso:** Quieres insertar una nueva pregunta o pantalla en medio de un flujo que ya existe.

> ⚠️ Este es el cambio más común y requiere tocar **4 lugares distintos**.

### Archivos a modificar (en orden)

| # | Archivo | Qué hacer |
|---|---------|-----------|
| 1 | `internal/statemachine/states.go` | Declarar el nuevo estado como constante |
| 2 | `internal/statemachine/states.go` (mapa `stateTypes`) | Registrar si es Automatic o Interactive |
| 3 | `internal/statemachine/handlers/<flujo>.go` | Escribir la función handler del nuevo estado |
| 4 | `internal/statemachine/handlers/<flujo>.go` | Registrar el handler en la función `RegisterXxxHandlers` |
| 5 | Archivo handler del estado **anterior** | Cambiar su `NextState` al nuevo estado |
| 6 | Archivo handler del nuevo estado | Apuntar su `NextState` al estado que seguía antes |

### Paso 1 — Declarar el estado en `states.go`

```go
// internal/statemachine/states.go
// Agrega dentro del grupo de constantes más cercano al flujo:
const (
    // ...estados existentes...
    StateAskExtraInfo = "ASK_EXTRA_INFO" // ← nuevo
)
```

### Paso 2 — Definir su tipo en `stateTypes`

```go
// En el mapa var stateTypes = map[string]StateType:
StateAskExtraInfo: StateTypeInteractive, // Espera respuesta del usuario
// o
StateAskExtraInfo: StateTypeAutomatic,   // Se ejecuta sin input
```

### Paso 3 — Escribir el handler

```go
// internal/statemachine/handlers/<flujo>.go

func askExtraInfoHandler() sm.StateHandler {
    return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
        input := strings.TrimSpace(msg.Text)
        
        if input == "" {
            return sm.NewResult(sess.CurrentState).
                WithText("Por favor responde la pregunta."), nil
        }
        
        return sm.NewResult(sm.StateNextState). // estado que va después
            WithContext("extra_info", input).
            WithEvent("extra_info_entered", nil), nil
    }
}
```

### Paso 4 — Registrar el handler

```go
// En la función RegisterXxxHandlers del mismo archivo:
func RegisterXxxHandlers(m *sm.Machine, ...) {
    // ...handlers existentes...
    m.Register(sm.StateAskExtraInfo, askExtraInfoHandler()) // ← agregar
}
```

### Paso 4b — Si el nuevo handler necesita una nueva dependencia (Service o Repo)

> Si tu handler necesita un servicio o repositorio que el flujo aún no recibe, debes:

1. Agregarlo como parámetro en `RegisterXxxHandlers(m *sm.Machine, nuevoSvc *services.NuevoService, ...)`
2. Pasar la dependencia al handler: `m.Register(sm.StateXxx, nuevoHandler(nuevoSvc))`
3. **En `cmd/server/main.go`**: actualizar la llamada a `handlers.RegisterXxxHandlers(machine, nuevoSvc, ...)`

```go
// cmd/server/main.go — actualizar la llamada correspondiente:
handlers.RegisterXxxHandlers(machine, nuevoSvc, repoExistente)
```

### Paso 5 y 6 — Reconectar estados

```go
// En el estado ANTERIOR (el que apuntaba al siguiente):
// ANTES:
return sm.NewResult(sm.StateNextState)...

// DESPUÉS:
return sm.NewResult(sm.StateAskExtraInfo)... // ← apunta al nuevo primero
```

```go
// En tu NUEVO estado, al final:
return sm.NewResult(sm.StateNextState)... // ← apunta al que seguía antes
```

### Checklist de verificación

- [ ] Nuevo estado en `states.go` como constante
- [ ] Nuevo estado en `stateTypes` (Automatic/Interactive)
- [ ] Handler escrito en el `.go` del flujo
- [ ] Handler registrado en `RegisterXxxHandlers`
- [ ] Estado anterior apunta al nuevo estado
- [ ] Nuevo estado apunta al siguiente estado correcto
- [ ] Si el estado es interactivo con botones, definir `Options` en `RegisterWithConfig`
- [ ] Si se guardan datos con `WithContext`, verificar que se lean con `sess.GetContext` en estados posteriores

---

## 4 — Crear un flujo nuevo completo

**Caso:** Quieres crear un flujo completamente nuevo (ej: "Agendar videollamada").

### Archivos a crear/modificar

| Archivo | Acción |
|---------|--------|
| `internal/statemachine/states.go` | Agregar todas las constantes de estados del flujo |
| `internal/statemachine/states.go` | Registrar todos los tipos en `stateTypes` |
| `internal/statemachine/handlers/<nuevo_flujo>.go` | Crear archivo nuevo con todos los handlers |
| `internal/statemachine/handlers/<flujo_de_entrada>.go` | Agregar opción en el menú que lleva al nuevo flujo |
| `cmd/server/main.go` (o donde se configura la máquina) | Llamar a `RegisterNuevoFlujoHandlers(m, ...)` |
| `internal/statemachine/states.go` | Agregar las constantes de los estados |

### Estructura del archivo de handlers nuevo

```go
// internal/statemachine/handlers/videocall.go
package handlers

import (
    "context"
    "github.com/neuro-bot/neuro-bot/internal/bird"
    "github.com/neuro-bot/neuro-bot/internal/session"
    sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
)

// RegisterVideocallHandlers registra todos los handlers del flujo de videollamada.
func RegisterVideocallHandlers(m *sm.Machine) {
    m.Register(sm.StateAskVideocallDate, askVideocallDateHandler())
    m.Register(sm.StateConfirmVideocall, confirmVideocallHandler())
    m.Register(sm.StateVideocallBooked, videocallBookedHandler())
}

func askVideocallDateHandler() sm.StateHandler {
    return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
        // lógica...
        return sm.NewResult(sm.StateConfirmVideocall).
            WithContext("videocall_date", msg.Text).
            WithText("Confirmando..."), nil
    }
}

// ... más handlers
```

### Conectar desde el menú principal

```go
// En handlers/greeting.go mainMenuHandler():
case "videollamada":
    return sm.NewResult(sm.StateAskVideocallDate).
        WithEvent("menu_selected", map[string]interface{}{"option": "videollamada"}), nil
```

### Registrar en el servidor

```go
// En cmd/server/main.go — agregar junto a los otros Register...:
handlers.RegisterVideocallHandlers(machine, videocallSvc) // pasar deps necesarias
```

### Si el nuevo flujo necesita un repo o servicio nuevo

1. Crea la interfaz en `internal/repository/interfaces.go`
2. Implementa el repo en `internal/repository/local/<repo>.go` (BD del bot) o `internal/repository/siesa/<repo>.go` (BD clínica SIESA)
3. Si es externo, agrégalo a `repository.Repositories` en `internal/repository/` y a `initRepositories()` en `main.go`
4. Si es local (tabla nueva del bot), instáncialo en `main.go` como: `myRepo := localrepo.NewMyRepo(localDB)`
5. Inyéctalo en `handlers.RegisterXxxHandlers(machine, myRepo, ...)`

---

## 5 — Eliminar un paso de un flujo

**Caso:** Quieres quitar una pantalla/pregunta de un flujo.

> ⚠️ Nunca elimines simplemente el handler — eso causa un panic en runtime si algún estado todavía apunta al estado eliminado.

### Pasos

1. **En el estado ANTERIOR**: cambia `NextState` para que apunte al estado que venía DESPUÉS del eliminado.
2. **Elimina el handler** del archivo de handlers.
3. **Elimina el `m.Register(...)` o `m.RegisterWithConfig(...)`** del `RegisterXxxHandlers`.
4. **Elimina la constante** del estado en `states.go`.
5. **Elimina del mapa `stateTypes`** en `states.go`.
6. **Busca con grep** que no quede ninguna referencia al estado eliminado en todo el codebase:
   ```powershell
   grep -r "StateEliminado" internal/
   ```

---

## 6 — Agregar una validación de texto nueva

**Caso:** Quieres validar que un campo de texto sea de un formato específico (ej: código postal, número de tarjeta).

### Archivos a modificar

| Archivo | Qué hacer |
|---------|-----------|
| `internal/statemachine/validators/validators.go` | Agregar la función de validación |
| `internal/statemachine/handlers/<flujo>.go` | Usar la nueva validación en el handler |

### Paso 1 — Agregar validador en `validators.go`

```go
// internal/statemachine/validators/validators.go

// PostalCode validates a 6-digit postal code.
func PostalCode(s string) bool {
    matched, _ := regexp.MatchString(`^\d{6}$`, s)
    return matched
}
```

### Paso 2 — Usarla en el handler con `RegisterWithConfig`

```go
m.RegisterWithConfig(sm.StateAskPostalCode, sm.HandlerConfig{
    InputType:    sm.InputText,
    TextValidate: validators.PostalCode,
    ErrorMsg:     "Ingresa un código postal válido de 6 dígitos.",
    Handler:      askPostalCodeHandler(),
})
```

### O usarla manualmente en un handler

```go
input := strings.TrimSpace(msg.Text)
retryResult := sm.ValidateWithRetry(sess, input, validators.PostalCode, "Código postal inválido.")
if retryResult != nil {
    return retryResult, nil
}
```

### Paso 3 — Agregar el test en `validators_test.go`

El archivo `internal/statemachine/validators/validators_test.go` ya existe. Agrega casos de prueba:

```go
// En validators_test.go:
func TestPostalCode(t *testing.T) {
    tests := []struct {
        input string
        want  bool
    }{
        {"110111", true},   // válido
        {"ABC123", false},  // letras
        {"12345", false},   // solo 5 dígitos
    }
    for _, tt := range tests {
        if got := PostalCode(tt.input); got != tt.want {
            t.Errorf("PostalCode(%q) = %v, want %v", tt.input, got, tt.want)
        }
    }
}
```

---

## 7 — Cambiar el horario de atención

**Caso:** El horario de atención del centro médico cambió.

### Archivo a modificar

```
internal/statemachine/handlers/greeting.go
```

### Función a editar: `checkBusinessHoursHandler`

```go
func checkBusinessHoursHandler(cfg *config.Config) sm.StateHandler {
    return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
        if cfg.TestingAlwaysOpen {
            return sm.NewResult(sm.StateGreeting), nil
        }

        now := nowFunc()
        weekday := now.Weekday()
        hour := now.Hour()

        inHours := false
        switch weekday {
        case time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday:
            inHours = hour >= 7 && hour < 18  // ← cambiar aquí (7am–6pm)
        case time.Saturday:
            inHours = hour >= 7 && hour < 12  // ← cambiar aquí (7am–12pm)
        // Para agregar domingo:
        // case time.Sunday:
        //     inHours = hour >= 8 && hour < 14
        }
        // ...
    }
}
```

> **Nota:** Los horarios están en zona horaria `America/Bogota`. El servidor debe tener `TZ=America/Bogota` configurado en Docker o en el ambiente.

---

## 8 — Agregar un nuevo campo en el registro de paciente

**Caso:** Quieres capturar información adicional del paciente en el formulario de registro.

### Archivos a modificar

| # | Archivo | Qué hacer |
|---|---------|-----------|
| 1 | `internal/statemachine/states.go` | Declarar el nuevo estado `StateRegNuevoCampo` |
| 2 | `internal/statemachine/states.go` | Agregarlo en `stateTypes` como `StateTypeInteractive` |
| 3 | `internal/statemachine/handlers/registration.go` | Escribir el handler del nuevo campo |
| 4 | `internal/statemachine/handlers/registration.go` | Registrarlo en `RegisterRegistrationHandlers` |
| 5 | `internal/statemachine/handlers/registration.go` | Reconectar el estado anterior → nuevo → siguiente |
| 6 | `internal/statemachine/handlers/registration.go` | Agregar el campo en `buildRegistrationSummary` |
| 7 | `internal/statemachine/handlers/registration.go` | Agregar el campo al `domain.CreatePatientInput` en `createPatientHandler` |
| 8 | `internal/domain/patient.go` | Agregar el campo en la struct `CreatePatientInput` |
| 9 | `internal/services/patient_service.go` | Mapear el nuevo campo al llamado a BD |

### Ejemplo: agregar campo "Número de afiliación"

```go
// 1 y 2 — states.go
const StateRegAffiliationNumber = "REG_AFFILIATION_NUMBER"
// en stateTypes:
StateRegAffiliationNumber: StateTypeInteractive,

// 3 y 4 — registration.go
m.Register(sm.StateRegAffiliationNumber, regFieldHandler(
    "reg_affiliation_number",
    "Ingresa tu número de afiliación:",
    validateNotEmpty,
    sm.StateConfirmRegistration, // o el siguiente estado
))

// 6 — en buildRegistrationSummary:
"🔢 Afiliación: %s\n", sess.GetContext("reg_affiliation_number"),

// 7 — en createPatientHandler:
AffiliationNumber: sess.GetContext("reg_affiliation_number"),
```

---

## 9 — Cambiar la lógica del scheduler (tareas automáticas)

**Caso:** Quieres cambiar la hora de una tarea, agregar una tarea nueva o modificar qué hace.

### Archivos a modificar

| Archivo | Qué cambiar |
|---------|-------------|
| `internal/scheduler/tasks.go` | Lógica de las tareas existentes o nueva función de tarea |
| `internal/scheduler/scheduler.go` | Si cambias hora/días de una tarea, edita el `RegisterAll` |

### Cambiar la hora de ejecución

```go
// internal/scheduler/tasks.go — en RegisterAll:
s.AddTask(ScheduledTask{
    Name: "whatsapp_reminders",
    Hour: 8, Minute: 30,  // ← cambiar aquí (era 7:00, ahora 8:30)
    Weekdays: []time.Weekday{
        time.Monday, time.Tuesday, time.Wednesday,
        time.Thursday, time.Friday, time.Saturday,
    },
    Fn: t.sendWhatsAppReminders,
})
```

### Agregar una tarea nueva

```go
// 1. Declarar la tarea en RegisterAll:
s.AddTask(ScheduledTask{
    Name:     "daily_report",
    Hour:     18, Minute: 0,
    Weekdays: []time.Weekday{time.Friday},
    Fn:       t.sendDailyReport,
})

// 2. Implementar la función:
func (t *Tasks) sendDailyReport(ctx context.Context) error {
    // lógica...
    return nil
}
```

### Agregar dependencias al scheduler

Si tu nueva tarea necesita nuevas dependencias (ej: un nuevo repositorio):

```go
// internal/scheduler/tasks.go — en el struct Tasks:
type Tasks struct {
    // ...existentes...
    ReportRepo ReportRepository // ← nueva dependencia
}

// Y en cmd/server/main.go donde se inicializa Tasks:
tasks := scheduler.Tasks{
    // ...existentes...
    ReportRepo: reportRepo, // ← inyectar
}
```

---

## 10 — Agregar una notificación proactiva nueva

**Caso:** Quieres que el bot envíe por iniciativa propia un nuevo tipo de mensaje (ej: "Tu resultado está listo").

Un flujo proactivo requiere: **enviar el template → registrar pendiente → manejar la respuesta → manejar el timeout**.

### Archivos a modificar

| # | Archivo | Qué hacer |
|---|---------|----------|
| 1 | `internal/api/webhook_handler.go` | Agregar los nuevos payloads a `isNotificationPostback()` |
| 2 | `internal/notifications/manager.go` | Agregar el nuevo tipo en `HandleResponse` y `handleTimeout` |
| 3 | `internal/notifications/<tipo>.go` | Crear archivo nuevo con `handleNuevoTipo` y `handleNuevoTipoTimeout` |
| 4 | `internal/scheduler/tasks.go` | Agregar la tarea que envía el template y llama a `RegisterPending` |
| 5 | Bird Dashboard | Crear la plantilla de WhatsApp con sus botones → obtener ProjectID/VersionID |
| 6 | `.env` | Agregar las variables del nuevo template |
| 7 | `internal/config/config.go` | Leer las nuevas variables de entorno |

> ⚠️ **Paso 1 es crítico y fácil de olvidar.** La función `isNotificationPostback()` en `webhook_handler.go` actúa como portero: decide si un postback se rutea al `NotificationManager` o al chatbot normal. Si no registras los payloads nuevos aquí, las respuestas del usuario al template irán al flujo de chatbot en lugar de a tu handler de notificación.

### Paso a paso

```go
// PASO 1 — api/webhook_handler.go: agregar los payloads del nuevo template
func isNotificationPostback(payload string) bool {
    switch payload {
    case "confirm", "cancelar", "cancel", "understood",
         "reschedule", "wl_schedule", "wl_decline",
         "view_result", "dismiss":  // ← agregar los nuevos payloads aquí
        return true
    default:
        return false
    }
}

// PASO 2 — notifications/manager.go: agregar en HandleResponse
switch pending.Type {
case "confirmation":
    m.handleConfirmation(phone, normalized, pending)
case "result_ready":                                  // ← nuevo
    m.handleResultReady(phone, normalized, pending)   // ← nuevo
}

// PASO 3 — notifications/manager.go: agregar en handleTimeout
case "result_ready":
    m.handleResultReadyTimeout(pending)

// PASO 4 — Crear internal/notifications/result_ready.go:
func (m *NotificationManager) handleResultReady(phone, payload string, pending *PendingNotification) {
    switch payload {
    case "view_result":
        // crear sesión y enrutar al flujo de resultados...
    case "dismiss":
        // solo loguear que fue descartado
    }
}

func (m *NotificationManager) handleResultReadyTimeout(pending *PendingNotification) {
    // qué hacer si no respondió en 6 horas
}

// PASO 5 — scheduler/tasks.go: enviar el template y registrar pendiente:
msgID, _ := t.BirdClient.SendTemplate(phone, bird.TemplateConfig{
    ProjectID: t.Cfg.BirdTemplateResultProjectID,
    VersionID: t.Cfg.BirdTemplateResultVersionID,
    Locale:    "es",
    Params: []bird.TemplateParam{
        {Type: "string", Key: "patient_name", Value: patientName},
    },
})

t.NotifyManager.RegisterPending(notifications.PendingNotification{
    Type:          "result_ready",
    Phone:         phone,
    AppointmentID: apptID,
    BirdMessageID: msgID,
})
```

---

## 11 — Agregar una variable de entorno nueva

**Caso:** Tu nuevo feature necesita una configuración externa (API key, URL, flag, etc.).

### Archivos a modificar

| # | Archivo | Qué hacer |
|---|---------|-----------|
| 1 | `internal/config/config.go` | Agregar el campo en la struct `Config` y leerlo en `Load()` |
| 2 | `.env` | Agregar el valor real (no commitear claves reales) |
| 3 | `.env.example` | Agregar la variable con un valor de ejemplo o descripción |

### Ejemplo

```go
// internal/config/config.go

type Config struct {
    // ...campos existentes...
    MyServiceAPIKey string // ← nuevo
    MyServiceURL    string // ← nuevo
}

func Load() *Config {
    // ...
    return &Config{
        // ...
        MyServiceAPIKey: os.Getenv("MY_SERVICE_API_KEY"),                       // ← nuevo
        MyServiceURL:    getEnv("MY_SERVICE_URL", "https://api.example.com"),   // ← nuevo con default
    }
}
```

```env
# .env.example
MY_SERVICE_API_KEY=tu-api-key-aqui
MY_SERVICE_URL=https://api.example.com
```

> Después de agregar la variable al `Config`, inyecta `cfg` en el lugar donde la necesitas. El `cfg` ya se pasa a la mayoría de los handlers.

---

## 12 — Agregar una nueva tabla en la base de datos

**Caso:** Tu nuevo feature necesita persistir datos en la base de datos local del bot.

### Archivos a crear/modificar

| # | Archivo | Qué hacer |
|---|---------|-----------|
| 1 | `migrations/XXX_create_<tabla>.up.sql` | Crear la migración de creación |
| 2 | `migrations/XXX_create_<tabla>.down.sql` | Crear la migración de rollback |
| 3 | `internal/domain/<entidad>.go` | Crear la struct de dominio |
| 4 | `internal/repository/interfaces.go` | Agregar la interfaz (para tablas locales, en el mismo archivo) |
| 5 | `internal/repository/local/<tabla>.go` | Implementar la interfaz |
| 6 | `cmd/server/main.go` | Instanciar el repo e inyectarlo donde se usa |

> **¿Es una tabla de la BD externa (SIESA / SQL Server)?** En ese caso también debes:
> - Agregar el campo a `repository.Repositories` en `internal/repository/`
> - Agregar el repo a `initRepositories()` en `main.go` (`repos.MiTabla = siesa.NewMiTablaRepo(externalDB)`)
> - Implementar el repo en `internal/repository/siesa/<tabla>.go`

### Convención de nombres de migración

Los archivos siguen el patrón `NNN_descripcion.up.sql` donde `NNN` es el número secuencial (siguiendo el último existente):

```
009_create_notification_pending.up.sql   ← último existente
010_create_results_cache.up.sql          ← nuevo
010_create_results_cache.down.sql
```

### Ejemplo de migración

```sql
-- 010_create_results_cache.up.sql
CREATE TABLE results_cache (
    id          VARCHAR(36) PRIMARY KEY,
    patient_id  VARCHAR(50) NOT NULL,
    result_url  TEXT        NOT NULL,
    created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    expires_at  TIMESTAMP NOT NULL,
    INDEX idx_patient_id (patient_id)
);
```

```sql
-- 010_create_results_cache.down.sql
DROP TABLE IF EXISTS results_cache;
```

```bash
# Aplicar la migración:
make migrate-up
```

---

## 13 — Modificar acceso a datos (repository)

**Caso:** Quieres agregar una nueva consulta o escritura a la base de datos.

### Archivos a modificar

| Archivo | Qué hacer |
|---------|-----------|
| `internal/repository/interfaces.go` | Agregar el método a la interfaz del repositorio |
| `internal/repository/local/<repo>.go` o `internal/repository/siesa/<repo>.go` | Implementar el método |
| Cualquier archivo de test (`_test.go`) | Actualizar el mock si existe |

### Estructura de un método de repositorio

```go
// interfaces.go — agregar a la interfaz:
type ResultsCacheRepository interface {
    FindByPatient(ctx context.Context, patientID string) ([]domain.ResultCache, error)
    Create(ctx context.Context, entry *domain.ResultCache) error
    DeleteExpired(ctx context.Context) (int64, error)
}

// local/results_cache.go — implementación:
type MySQLResultsCacheRepo struct {
    db *sql.DB
}

func (r *MySQLResultsCacheRepo) FindByPatient(ctx context.Context, patientID string) ([]domain.ResultCache, error) {
    rows, err := r.db.QueryContext(ctx,
        "SELECT id, result_url, created_at FROM results_cache WHERE patient_id = ?",
        patientID)
    if err != nil {
        return nil, fmt.Errorf("query results_cache: %w", err)
    }
    defer rows.Close()

    var results []domain.ResultCache
    for rows.Next() {
        var r domain.ResultCache
        rows.Scan(&r.ID, &r.ResultURL, &r.CreatedAt)
        results = append(results, r)
    }
    return results, nil
}
```

> **Regla importante**: Toda consulta debe recibir `ctx context.Context` como primer parámetro y pasar errores hacia arriba con `fmt.Errorf("contexto: %w", err)`.

---

## 14 — Agregar un interceptor global (keyword especial)

**Caso:** Quieres que el bot responda a una palabra clave en cualquier estado de la conversación (ej: "cancelar", "reiniciar", "agente").

Los interceptores se ejecutan **antes** del handler normal y pueden "secuestrar" el mensaje.

### Archivos a modificar

| Archivo | Qué hacer |
|---------|-----------|
| `internal/statemachine/interceptors.go` | Agregar la función interceptora Y registrarla en `RegisterInterceptors` |

> `RegisterInterceptors()` es la función que `main.go` ya llama — es el lugar centralizado para registrar todos los interceptores. **No es necesario tocar `main.go`** para agregar interceptores.

### Ejemplo: interceptor para la palabra "reiniciar"

```go
// internal/statemachine/interceptors.go

// 1. Definir el interceptor:
func MyNewInterceptor() Interceptor {
    return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*StateResult, bool) {
        if strings.ToLower(strings.TrimSpace(msg.Text)) == "reiniciar" {
            return NewResult(StateCheckBusinessHours).
                WithEvent("user_restarted", map[string]interface{}{
                    "from_state": sess.CurrentState,
                }).
                WithText("Reiniciando la conversación..."), true
        }
        return nil, false
    }
}

// 2. Registrarlo en RegisterInterceptors (misma archivo):
func RegisterInterceptors(machine *Machine) {
    machine.AddInterceptor(UnsupportedMessageInterceptor())
    machine.AddInterceptor(ImageOutOfContextInterceptor())
    machine.AddInterceptor(EscalationKeywordsInterceptor())
    machine.AddInterceptor(MenuResetInterceptor())
    machine.AddInterceptor(MyNewInterceptor()) // ← agregar aquí
}
```

> **Orden de interceptores**: Se ejecutan en el orden registrado en `RegisterInterceptors`. El primero que retorna `true` detiene la cadena. Coloca los más prioritarios primero (la escalación ya está antes que el reset de menú intencionalmente).

---

## 15 — Cambiar cuántos reintentos antes de escalar a agente

**Caso:** Quieres que el bot tolere más o menos errores antes de transferir al usuario a un agente humano.

El número de reintentos **ya es configurable** por variable de entorno `MAX_RETRIES` (default: 4). Se lee en `config.go` y se aplica en `main.go` vía `SetMaxRetries`.

### Cómo funciona

```
.env                               → MAX_RETRIES=4
internal/config/config.go          → MaxRetries int (leído de env)
cmd/server/main.go                 → statemachine.SetMaxRetries(cfg.MaxRetries)
internal/statemachine/helpers.go   → var maxRetries = 3 (default, sobreescrito por SetMaxRetries)
```

### Para cambiar el valor

Simplemente edita `.env`:

```env
MAX_RETRIES=5
```

### Si necesitas lógica más granular (diferente por flujo)

El valor es global. Si algún flujo necesita un límite diferente, maneja el conteo manualmente en el handler en vez de usar `ValidateWithRetry`:

```go
count := sess.GetContextInt("my_custom_retry_count")
if count >= 2 { // límite personalizado para este paso
    return sm.NewResult(sm.StateEscalate), nil
}
sess.SetContext("my_custom_retry_count", strconv.Itoa(count+1))
```

---

## 16 — Agregar o quitar una keyword de menú o escalación

**Caso:** Quieres que al escribir una palabra como "salir" el bot también reinicie el menú, o que "ayuda" ya no escale.

### Archivo a modificar

```
internal/statemachine/interceptors.go
```

### Interceptor de menú (palabras que reinician la conversación)

```go
// En MenuResetInterceptor():
keywords := map[string]bool{
    "menu": true, "menú": true, "inicio": true, "reiniciar": true, "0": true,
    "salir": true,   // ← agregar
    "volver": true,  // ← agregar
}
```

### Interceptor de escalación (palabras que transfieren a agente)

```go
// En EscalationKeywordsInterceptor():
keywords := map[string]bool{
    "agente": true, "asesor": true, "humano": true,
    // "ayuda": true,  ← comentar para quitarla
    "operador": true, // ← agregar
}
```

> **Orden de prioridad**: La escalación se registra ANTES que el reset de menú. Si una palabra está en ambos mapas, ganará la escalación.

---

## 17 — Agregar un tipo de mensaje no soportado

**Caso:** Un nuevo tipo de mensaje de WhatsApp está llegando al bot (ej: encuestas, botones de catálogo) y quieres rechazarlo con un mensaje claro.

### Archivo a modificar

```
internal/statemachine/interceptors.go
```

### Función a editar: `UnsupportedMessageInterceptor`

```go
// Agrega el nuevo tipo al mapa:
unsupported := map[string]bool{
    "audio":    true,
    "video":    true,
    "location": true,
    "contact":  true,
    "sticker":  true,
    "order":    true,   // ← nuevo: pedidos de catálogo
    "poll":     true,   // ← nuevo: encuestas
}
```

Si quieres un **mensaje diferente** según el tipo:

```go
// Cambia el interceptor a un switch en vez de mapa:
switch msg.MessageType {
case "audio", "video":
    msg := "No puedo procesar archivos de audio/video."
    return NewResult(sess.CurrentState).WithText(msg), true
case "location":
    msg := "No puedo procesar ubicaciones. Escribe tu dirección en texto."
    return NewResult(sess.CurrentState).WithText(msg), true
}
```

### También revisar

Si quieres que un estado específico SÍ acepte ese tipo (como imágenes en `UPLOAD_MEDICAL_ORDER`), el patrón es el de `ImageOutOfContextInterceptor`: deja pasar el tipo solo cuando `sess.CurrentState == StateXxx`.

---

## 18 — Modificar campos directos del modelo de Sesión

**Caso:** Necesitas guardar un dato importante que sea parte de la sesión en sí (no del contexto libre), por ejemplo un campo que se busca frecuentemente en BD.

> Los campos del contexto (`SetContext`/`GetContext`) son pares clave-valor sin tipado fuerte. Los campos directos de `Session` son tipados y están en la tabla `sessions` de BD.

### Cuándo usar un campo directo vs contexto

| Campo directo en `Session` | Contexto (`session_context`) |
|---------------------------|-----------------------------|
| Se consulta desde SQL frecuentemente | Solo se usa dentro de handlers |
| Necesita tipo específico (int, time.Time) | Siempre es string |
| Ejemplo: `PatientID`, `PatientName` | Ejemplo: `cups_code`, `reg_address` |

### Archivos a modificar

| # | Archivo | Qué hacer |
|---|---------|-----------|
| 1 | `internal/session/types.go` | Agregar el campo en la struct `Session` |
| 2 | `internal/repository/local/sessions.go` | Agregar el campo en los queries INSERT, UPDATE y SELECT/scan |
| 3 | `migrations/NNN_add_campo_to_sessions.up.sql` | `ALTER TABLE sessions ADD COLUMN ...` |
| 4 | `migrations/NNN_add_campo_to_sessions.down.sql` | `ALTER TABLE sessions DROP COLUMN ...` |

```go
// 1. internal/session/types.go — agregar en Session struct:
PatientPhone string  // ← nuevo campo directo

// 2. internal/repository/local/sessions.go — actualizar 3 lugares:
// a) En el INSERT:
"INSERT INTO sessions (..., patient_phone) VALUES (..., ?)"
// b) En el UPDATE:
"UPDATE sessions SET ..., patient_phone=? WHERE ..."
// c) En el SELECT / rows.Scan:
rows.Scan(..., &s.PatientPhone)

// 3. En los handlers, asignar directamente al campo:
sess.PatientPhone = patient.Phone  // no usar sess.SetContext
```

---

## 19 — Agregar un comando de agente (/bot)

**Caso:** Quieres que los agentes humanos en Bird Inbox puedan ejecutar una acción especial escribiendo un comando en el chat (ej: `/bot asignar EPS123`).

Los agentes pueden escribir `/bot <accion> [datos]` y el bot los procesa.

### Archivos a modificar

| Archivo | Qué hacer |
|---------|-----------|
| `internal/worker/` (archivo de commands) | Agregar el nuevo `case` de acción |
| `internal/api/webhook_handler.go` | No tocar — ya pasa todos los `/bot` al worker pool |

### Cómo funciona el flujo de comandos

1. El agente escribe en Bird Inbox: `/bot reanudar paciente:12345`
2. Bird dispara un webhook **outbound** a `/webhook/bird/outbound`
3. `webhook_handler.go → handleOutbound` detecta el prefijo `/bot` y llama a `worker.ParseAgentCommand(text)`
4. El worker pool procesa el comando

### Agregar un nuevo comando

```go
// En el archivo de commands del worker pool:
switch cmd.Action {
case "reanudar":
    // lógica para reanudar sesión
case "asignar":            // ← nuevo
    // lógica para asignar entidad manualmente
    patientID := cmd.Data["paciente"]
    entity := cmd.Data["entidad"]
    // ...
}
```

**Formato del comando** que escribe el agente en Bird Inbox:
```
/bot <accion> <clave1>:<valor1> <clave2>:<valor2>
```

---

## 20 — Cambiar el timeout de inactividad de sesión

**Caso:** Quieres que el bot cierre la conversación antes (o después) si el usuario no responde.

El timeout de inactividad **no está en un solo lugar** — hay dos mecanismos:

### 1. Expiración de sesión (tiempo máximo de vida)

Se define cuando se crea la sesión. Busca en el repositorio de sesiones donde se asigna `ExpiresAt`.

```go
// Típicamente en internal/repository/local/sessions.go o en el worker:
sess.ExpiresAt = time.Now().Add(30 * time.Minute) // ← cambiar aquí
```

### 2. Recordatorio de inactividad (mensaje de "¿sigues ahí?")

El comportamiento de inactividad está controlado por **variables de entorno** leídas en `config.go` e inyectadas en `session.StartInactivityChecker()` desde `main.go`.

```go
// En main.go (lectura real del código):
go sessionManager.StartInactivityChecker(ctx, session.InactivityDeps{
    BirdClient:  birdClient,
    Tracker:     tracker,
    ReminderMin: cfg.InactivityReminderMin, // ← minutos para recordatorio único
    CloseMin:    cfg.InactivityCloseMin,    // ← minutos para cierre silencioso
})
```

```go
// internal/config/config.go — campos a cambiar:
InactivityReminderMin int // ej: INACTIVITY_REMINDER_MIN=5
InactivityCloseMin    int // ej: INACTIVITY_CLOSE_MIN=15
```

```env
# .env — cambiar los valores:
INACTIVITY_REMINDER_MIN=5
INACTIVITY_CLOSE_MIN=15
```

---

## 21 — Agregar evento de auditoría (tracking)

**Caso:** Quieres registrar un evento nuevo en el log de auditoría (tabla `chat_events`) para analítica o debugging.

Los eventos se agregan al `StateResult` con `.WithEvent()` — son automáticamente persistidos por el worker pool.

### En cualquier handler

```go
return sm.NewResult(sm.StateNext).
    WithText("Mensaje al usuario").
    WithEvent("nombre_del_evento", map[string]interface{}{
        "dato_relevante": valor,
        "otro_dato":      otro,
    }), nil
```

### Convención de nombres de eventos

| Prefijo | Cuándo usar |
|---------|-------------|
| `menu_selected` | El usuario eligió del menú principal |
| `patient_*` | Eventos relacionados con el paciente |
| `appointment_*` | Eventos de citas |
| `booking_*` | Proceso de agendamiento |
| `waiting_list_*` | Lista de espera |
| `notification_*` | Notificaciones proactivas |
| `max_retries_reached` | Límite de reintentos alcanzado |
| `invalid_input` | Input no válido en un paso |

> Los eventos se deberían usar para responder preguntas de negocio: ¿cuántos pacientes abandonan el flujo en cada paso? ¿cuál es el procedimiento más agendado? etc.

---

## 22 — Cambiar la lógica de agrupación de procedimientos

**Caso:** El bot agrupa varios procedimientos de una orden médica (ej: cuando el OCR detecta múltiples CUPS) y necesitas cambiar cómo se agrupan o cuántos se pueden agendar juntos.

### Archivo a modificar

```
internal/services/procedure_grouper.go
```

Este servicio recibe una lista de CUPS y los agrupa según reglas de negocio (compatibilidad, mismo día, etc.). Las reglas de agrupación están **hardcodeadas** aquí.

### Lo que hace cada función

- `GroupProcedures(cups []CUP) []CUPSGroup`: Divide los CUPS en grupos que se deben agendar por separado.
- Cada `CUPSGroup` tiene un campo `Espacios` que indica cuántos slots consecutivos necesita.

### Cuándo tocar este archivo

- Cuando un nuevo tipo de procedimiento necesita reglas especiales de agendamiento.
- Cuando cambia el número de espacios que requiere un procedimiento.
- Cuando dos procedimientos que antes eran incompatibles ahora se pueden agendar juntos.

---

## 23 — Agregar un nuevo endpoint HTTP

**Caso:** Quieres exponer una nueva URL en el servidor (ej: un endpoint de salud, un webhook nuevo de Bird, una API de administración).

### Archivos a modificar

| Archivo | Qué hacer |
|---------|-----------|
| `internal/api/` | Crear el nuevo handler HTTP |
| `cmd/server/main.go` | Registrar la ruta en el servidor HTTP |

### Estructura de un handler HTTP

```go
// internal/api/admin_handler.go
package api

import "net/http"

type AdminHandler struct {
    // dependencias necesarias
}

func (h *AdminHandler) HandleGetStatus(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    w.Write([]byte(`{"status":"ok"}`))
}
```

### Registrar la ruta

```go
// cmd/server/main.go (o donde se configura el mux HTTP):
http.HandleFunc("/admin/status", adminHandler.HandleGetStatus)
```

### Para endpoints de webhook de Bird (nuevo evento)

Si Bird va a enviar un nuevo tipo de webhook (ej: estado de delivery de mensajes):

1. Crea el handler HTTP en `internal/api/`.
2. Regístralo en el mux HTTP.
3. En el **Dashboard de Bird**: agrega una nueva suscripción de webhook apuntando a la nueva URL con la firma HMAC correspondiente.
4. Verifica la firma con `birdClient.VerifyWebhookSignature(...)` igual que los webhooks existentes.
5. Agrega la signing key del nuevo webhook al `.env` y al `Config`.

---

## 24 — Cambiar el mensaje cuando el usuario envía un tipo de input inesperado

**Caso:** Quieres cambiar lo que dice el bot cuando un usuario envía audio, video, ubicación u otro tipo de mensaje que el bot no soporta.

### Archivo a modificar

```
internal/statemachine/interceptors.go
— función: UnsupportedMessageInterceptor
```

```go
// ANTES:
WithText("⚠️ Solo puedo procesar mensajes de texto y fotos de órdenes médicas. Por favor, envía tu respuesta como texto.")

// DESPUÉS:
WithText("⚠️ Solo acepto mensajes de texto o fotos. Por favor escribe tu respuesta.")
```

Si quieres cambiar el mensaje cuando el usuario envía una imagen FUERA del contexto esperado:

```
— función: ImageOutOfContextInterceptor
```

```go
// ANTES:
WithText("No esperaba una imagen en este momento. Si necesitas enviar una orden médica, primero selecciona la opción de agendar cita.")

// DESPUÉS:
WithText("Solo acepto imágenes cuando estás en el paso de cargar tu orden médica.")
```

---

## 25 — Cambiar la concurrencia del worker pool

**Caso:** El servidor está bajo alta carga y quieres ajustar cuántos mensajes puede procesar en paralelo, o hay problemas de concurrencia.

### Archivo a modificar

El tamaño del worker pool (número de goroutines paralelas) se configura al inicializar el pool, típicamente en `cmd/server/main.go`:

```go
// cmd/server/main.go (o donde se crea el worker pool):
workerPool := worker.NewMessageWorkerPool(
    workerCount,   // ← número de goroutines paralelas
    queueSize,     // ← tamaño de la cola de mensajes
    // ...otras dependencias
)
```

### Regla de negocio de concurrencia

> **El bot procesa exactamente 1 mensaje por número de teléfono en paralelo** — esto lo garantiza el `PhoneMutex` en `internal/session/phone_mutex.go`. Por más goroutines que haya, dos mensajes del mismo usuario nunca se procesan simultáneamente.

Por eso:
- Aumentar `workerCount` mejora el **throughput de usuarios distintos** (más usuarios simultáneos).
- No ayuda a acelerar las respuestas de un mismo usuario.

---

## 26 — Agregar o modificar un feature flag

**Caso:** Quieres habilitar o deshabilitar una funcionalidad del bot sin cambiar código (ej: desactivar enrutamiento por equipo, desactivar límites CUPS).

### Archivos a modificar

| # | Archivo | Qué hacer |
|---|---------|-----------|
| 1 | `internal/config/config.go` | Agregar campo `bool` en la struct `Config` |
| 2 | `internal/config/config.go` | Leerlo en `Load()` con `getEnv("MI_FLAG", "true") == "true"` |
| 3 | `.env` y `.env.example` | Agregar la variable |
| 4 | Archivo donde aplica la lógica | Agregar guard `if !cfg.MiFlag { ... }` |

### Flags existentes como referencia

| Variable de entorno | Campo en Config | Default | Qué controla |
|---------------------|-----------------|---------|--------------|
| `CUPS_GROUP_LIMITS_ENABLED` | `CupsGroupLimitsEnabled` | `true` | Límites mensuales SOAT por grupo de CUPS |
| `TEAM_ROUTING_ENABLED` | `TeamRoutingEnabled` | `true` | Enrutamiento a Grupo A/B por especialidad |
| `TESTING_ALWAYS_OPEN` | `TestingAlwaysOpen` | `false` | Ignora horario de atención (solo pruebas) |

### Ejemplo: agregar flag para deshabilitar OCR

```go
// 1. Config struct:
OCREnabled bool // Habilita/deshabilita OCR de órdenes médicas

// 2. Load():
OCREnabled: getEnv("OCR_ENABLED", "true") == "true",

// 3. .env:
OCR_ENABLED=true

// 4. En el handler (medical_order.go):
if !cfg.OCREnabled {
    return sm.NewResult(sm.StateEscalate).
        WithText("El procesamiento de imágenes no está disponible."), nil
}
```

### Patrón de guard centralizado vs distribuido

- **Centralizado** (preferido): El flag se evalúa en un solo punto de decisión. Ejemplo: `TeamRoutingEnabled` se evalúa solo en `ResolveTeamForCups()`.
- **Distribuido**: El flag se evalúa en múltiples lugares. Ejemplo: `CupsGroupLimitsEnabled` se evalúa en `CheckSOATLimit`, `CheckSOATLimitForMonth`, y en el handler.

> Prefiere el patrón centralizado cuando sea posible — un solo punto de cambio es menos propenso a errores.

---

## 27 — Modificar los límites mensuales CUPS (grupos SOAT)

**Caso:** Quieres cambiar el límite mensual de un grupo de procedimientos, agregar un nuevo grupo, o agregar un código CUPS a un grupo existente.

### Archivo a modificar

```
internal/services/appointment_service.go
```

### Estructura del mapa `soatGroups`

```go
var soatGroups = map[string]soatGroup{
    "consulta_neurologia": {
        MaxPerMonth: 397,
        CupsCodes:   []string{"890274", "890374"},
    },
    "electroencefalograma": {
        MaxPerMonth: 172,
        CupsCodes:   []string{"895101", "895102", "895100"},
    },
    // ... más grupos
}
```

### Para cambiar un límite mensual

Edita `MaxPerMonth` del grupo correspondiente:

```go
"consulta_neurologia": {
    MaxPerMonth: 450, // ← era 397, ahora 450
    CupsCodes:   []string{"890274", "890374"},
},
```

### Para agregar un código CUPS a un grupo existente

Agrega el código al slice `CupsCodes`:

```go
"consulta_neurologia": {
    MaxPerMonth: 397,
    CupsCodes:   []string{"890274", "890374", "890275"}, // ← nuevo código
},
```

### Para crear un grupo nuevo

Agrega una nueva entrada al mapa:

```go
"nuevo_grupo": {
    MaxPerMonth: 100,
    CupsCodes:   []string{"123456", "123457"},
},
```

### Cómo funciona la validación

1. Cuando un paciente **SAN01** busca slots, el handler setea `soat_limit_check=1` en el contexto.
2. En `GetAvailableSlots`, se ejecuta un `MonthFilter` callback por cada mes que tiene agendas.
3. El callback llama a `CheckSOATLimitForMonth(ctx, cupsCode, entity, year, month)`.
4. Si el mes ya alcanzó el límite (`count >= MaxPerMonth`), se omite ese mes.
5. Si **todos** los meses están al límite, no hay slots → el paciente va a **lista de espera**.

### También aplica a notificaciones de lista de espera

Cuando se notifica a un paciente de WL (real-time o daily), el `MonthFilter` se aplica antes de mostrar disponibilidad. Esto evita notificar slots de meses que ya superaron el límite.

> **Importante:** Los límites solo aplican cuando `CupsGroupLimitsEnabled=true` Y la entidad del paciente es `SAN01`. Otros pacientes no tienen restricción.

---

## 28 — Modificar el enrutamiento de equipos por especialidad

**Caso:** Quieres cambiar qué equipo de Bird atiende un procedimiento específico, o agregar un nuevo tipo de procedimiento al enrutamiento.

### Archivo a modificar

```
internal/config/config.go → función ResolveTeamForCups()
```

### Estructura actual del enrutamiento

```go
func (c *Config) ResolveTeamForCups(cupsCode string) string {
    if !c.TeamRoutingEnabled {
        return c.BirdTeamFallback // Todo al Call Center
    }
    p3 := cupsCode[:3]
    switch {
    case p3 == "881" || p3 == "882":            // Ecografía → Grupo A
        return c.BirdTeamGrupoA
    case p3 == "883":                            // Resonancia → Grupo A
        return c.BirdTeamGrupoA
    case p3 == "871" || p3 == "879":            // TAC → Grupo A
        return c.BirdTeamGrupoA
    case p3 == "870" || (p3 >= "873" && p3 <= "878"): // RX → Grupo A
        return c.BirdTeamGrupoA
    case p3 == "291" || p3 == "930" || p3 == "892":   // EMG/Fisiatría → Grupo B
        return c.BirdTeamGrupoB
    case cupsCode == "890274" || cupsCode == "890374" || cupsCode == "053105": // Neurología → Grupo B
        return c.BirdTeamGrupoB
    default:
        return c.BirdTeamFallback // Call Center
    }
}
```

### Para mover un procedimiento a otro equipo

Cambia la línea correspondiente. Ejemplo: mover TAC de Grupo A a Grupo B:

```go
// ANTES:
case p3 == "871" || p3 == "879":
    return c.BirdTeamGrupoA

// DESPUÉS:
case p3 == "871" || p3 == "879":
    return c.BirdTeamGrupoB
```

### Para agregar un nuevo grupo (Grupo C)

1. **Config struct**: agregar `BirdTeamGrupoC string`
2. **Load()**: agregar `BirdTeamGrupoC: os.Getenv("BIRD_TEAM_GRUPO_C")`
3. **ResolveTeamForCups**: agregar cases que retornen `c.BirdTeamGrupoC`
4. **resolveTeamName** en `escalation.go`: agregar `case cfg.BirdTeamGrupoC: return "Grupo C (Nombre)"`
5. **.env**: agregar `BIRD_TEAM_GRUPO_C=<uuid-del-equipo-en-bird>`
6. **Tests**: agregar en `fixtures.go` y `escalation_test.go`

### Flujo de escalación

Cuando se escala al agente, `EscalateToAgent` en `bird/client.go` hace:
1. Busca agentes disponibles en el equipo primario
2. Elige al de menor carga (`pickLeastLoadedAgent`)
3. Si no hay agentes en el equipo primario → intenta el equipo fallback (Call Center)
4. Si tampoco hay → asigna al equipo sin agente específico

---

## 29 — Modificar el flujo de escalación de confirmación

**Caso:** Quieres cambiar los tiempos o pasos de la cadena de confirmación de citas (template WA → follow-up → IVR → escalación a agente).

### Archivo a modificar

```
internal/notifications/confirmation.go
```

### Cadena de escalación actual (4 pasos)

| Paso | RetryCount | Qué hace | Cuándo se ejecuta |
|------|------------|----------|-------------------|
| 0 | 0→1 | Follow-up #1: mensaje amigable (texto directo, NO agente) | `CONFIRMATION_FOLLOWUP_1_HOURS` después del template (default: 3h) |
| 1 | 1→2 | Follow-up #2: mensaje directo (texto, NO agente) | `CONFIRMATION_FOLLOWUP_2_HOURS` después del follow-up 1 (default: 3h) |
| 2 | 2→3 | Safety net: si IVR no se ejecutó, escala a agente | 2h + `CONFIRMATION_POST_IVR_MINUTES` + 30min buffer |
| 3 | ≥3 | Post-IVR: escalación normal a agente | `CONFIRMATION_POST_IVR_MINUTES` después de IVR (default: 30min) |

### Para cambiar los tiempos

Edita las variables de entorno:

```env
CONFIRMATION_FOLLOWUP_1_HOURS=3    # Horas antes del 1er follow-up
CONFIRMATION_FOLLOWUP_2_HOURS=3    # Horas antes del 2do follow-up
CONFIRMATION_POST_IVR_MINUTES=30   # Minutos después de IVR para escalar
```

### Para agregar o quitar un paso

En `handleConfirmationTimeout(pending)`:

```go
func (m *NotificationManager) handleConfirmationTimeout(pending *PendingNotification) {
    switch pending.RetryCount {
    case 0: // Follow-up 1
        m.sendFollowUp1(pending)
    case 1: // Follow-up 2
        m.sendFollowUp2(pending)
    case 2: // Safety net (si IVR falló)
        m.escalateToAgent(pending)
    default: // Post-IVR escalación
        m.escalateToAgent(pending)
    }
}
```

Para eliminar un paso, simplemente salta al siguiente `RetryCount`. Para agregar uno, agrega un nuevo `case` y ajusta los demás.

### Relación con IVR

El IVR (llamada telefónica) se ejecuta como tarea programada del scheduler a las 15:00. Después del IVR, el timeout se re-registra con `CONFIRMATION_POST_IVR_MINUTES` de espera. Si el IVR no se ejecutó (ej: fue fin de semana), el paso 2 actúa como safety net y escala directamente.

---

## 30 — Modificar el flujo de reprogramación por notificación

**Caso:** Quieres cambiar qué datos se pre-cargan cuando un paciente acepta reprogramar su cita desde una notificación de reschedule.

### Archivo a modificar

```
internal/notifications/self_reschedule.go
```

### Cómo funciona

Cuando el paciente responde "reschedule" al template de reprogramación:

1. Se busca la cita original (`apptID`) y sus procedimientos
2. Se crea una **nueva sesión** posicionada en el estado `SEARCH_SLOTS`
3. Se pre-carga el contexto de sesión con todos los datos necesarios
4. Se encola un **mensaje virtual** al worker pool para arrancar la búsqueda de slots

### Datos que se pre-cargan

```go
sess.SetContext("patient_id", patient.DocumentID)
sess.SetContext("patient_name", patient.FullName)
sess.SetContext("patient_entity", patient.EntityCode)
sess.SetContext("patient_age", "0")              // se recalcula después
sess.SetContext("cups_code", proc.CupsCode)
sess.SetContext("cups_name", proc.CupsName)
sess.SetContext("is_contrasted", contrasted)
sess.SetContext("is_sedated", sedated)
sess.SetContext("espacios", espacios)
sess.SetContext("menu_option", "agendar")
sess.SetContext("reschedule_appt_id", apptID)    // cita a reemplazar
sess.SetContext("reschedule_skip_cancel", "0")   // cancela la vieja al agendar
```

### Si el paciente tiene datos clínicos previos

Si la cita original tiene datos de GFR (función renal) o embarazo, se pre-cargan:

```go
sess.SetContext("gfr_creatinine", entry.GFRCreatinine)
sess.SetContext("gfr_calculated", entry.GFRCalculated)
sess.SetContext("is_pregnant", "1")
sess.SetContext("baby_weight_cat", entry.BabyWeightCat)
```

### Para agregar un dato nuevo a la pre-carga

1. Asegúrate de que el dato esté disponible en la cita original o en el paciente
2. Agrega `sess.SetContext("mi_dato", valor)` en `startSelfReschedule()`
3. Verifica que el handler de `SEARCH_SLOTS` (o el siguiente) lee ese dato con `sess.GetContext("mi_dato")`

---

## 31 — Modificar el flujo de lista de espera

**Caso:** Quieres cambiar cómo se notifica a pacientes de la lista de espera, cómo se crea la sesión al aceptar, o las condiciones para enviar la notificación.

### Archivos involucrados

| Archivo | Responsabilidad |
|---------|----------------|
| `internal/notifications/waiting_list_check.go` | Verificación en tiempo real (cuando se cancela una cita) |
| `internal/notifications/waiting_list.go` | Manejo de respuesta del paciente y timeout |
| `internal/scheduler/tasks.go` | Verificación diaria (barrido de WL pendientes) |
| `internal/statemachine/handlers/slots.go` | Ofrecimiento de WL cuando no hay slots |

### Flujo completo

```
1. No hay slots disponibles
   → Handler ofrece "¿Quieres entrar a la lista de espera?"
   → Paciente acepta → se guarda en tabla `waiting_list` (status: "waiting")

2. Otro paciente cancela una cita del mismo CUPS
   → waiting_list_check.go: busca el primero en FIFO
   → Verifica: ¿ya tiene cita futura para ese CUPS? → skip si sí
   → Verifica: ¿hay slots disponibles? (con MonthFilter si SAN01)
   → Envía template WL → marca como "notified"

3. Paciente responde al template WL
   → "wl_schedule": crea sesión en SEARCH_SLOTS con datos pre-cargados
   → "wl_decline": marca como "declined"

4. Timeout (6h sin respuesta)
   → Marca como "expired"
```

### Para cambiar las condiciones de notificación

En `CheckWaitingListForCups()` de `waiting_list_check.go`:

```go
// Agregar nueva condición antes de enviar:
if miNuevaCondicion {
    slog.Info("wl_check: skipping", "reason", "mi_razon")
    return 0
}
```

### Para cambiar el timeout

El timeout de WL es de 6 horas, definido al llamar `RegisterPending()`. El valor está en `notifications/manager.go` donde se configura el timer.

### Para cambiar los datos pre-cargados en la sesión WL

En `handleWaitingList()` de `waiting_list.go`, modifica los `sess.SetContext()` que se ejecutan cuando el paciente acepta (`wl_schedule`):

```go
// Se pre-cargan TODOS estos datos desde la entrada de WL:
sess.SetContext("cups_code", entry.CupsCode)
sess.SetContext("cups_name", entry.CupsName)
sess.SetContext("patient_entity", entry.PatientEntity)
sess.SetContext("is_contrasted", entry.IsContrasted)
sess.SetContext("is_sedated", entry.IsSedated)
sess.SetContext("espacios", entry.Espacios)
sess.SetContext("waiting_list_entry_id", entry.ID)
// ... más campos de GFR, embarazo, etc.
```

---

## 32 — Agregar o modificar un template de Bird (WhatsApp)

**Caso:** Quieres usar un nuevo template de WhatsApp para enviar mensajes proactivos, o cambiar los parámetros de uno existente.

### Patrón de configuración de templates

Cada template requiere 3 variables de entorno:

```env
BIRD_TEMPLATE_<TIPO>_PROJECT_ID=<uuid>    # ID del proyecto en Bird
BIRD_TEMPLATE_<TIPO>_VERSION_ID=<uuid>    # ID de la versión aprobada
BIRD_TEMPLATE_<TIPO>_LOCALE=es-CO         # Idioma/locale del template
```

### Templates actuales

| Tipo | Propósito | Parámetros |
|------|-----------|------------|
| `CONFIRM` | Confirmación de cita | patient_name, procedure_name, date, time, doctor_name, location |
| `RESCHEDULE` | Reprogramación | patient_name, procedure_name, date, time |
| `WAITING_LIST` | Disponibilidad en WL | patient_name, procedure_name, cups_code |
| `CANCELLATION` | Cancelación | patient_name, procedure_name, date, time |

### Para agregar un template nuevo

1. **Crear el template en Bird Dashboard** → obtener ProjectID y VersionID
2. **Config struct** (`config.go`):
   ```go
   BirdTemplateNuevoProjectID  string
   BirdTemplateNuevoVersionID  string
   BirdTemplateNuevoLocale     string
   ```
3. **Load()** (`config.go`):
   ```go
   BirdTemplateNuevoProjectID: os.Getenv("BIRD_TEMPLATE_NUEVO_PROJECT_ID"),
   BirdTemplateNuevoVersionID: os.Getenv("BIRD_TEMPLATE_NUEVO_VERSION_ID"),
   BirdTemplateNuevoLocale:    getEnv("BIRD_TEMPLATE_NUEVO_LOCALE", "es-CO"),
   ```
4. **.env y .env.example**: agregar las 3 variables
5. **.env.testing**: agregar los IDs del canal de pruebas
6. **Enviar el template** donde lo necesites:
   ```go
   tmpl := bird.TemplateConfig{
       ProjectID: cfg.BirdTemplateNuevoProjectID,
       VersionID: cfg.BirdTemplateNuevoVersionID,
       Locale:    cfg.BirdTemplateNuevoLocale,
       Params: []bird.TemplateParam{
           {Type: "string", Key: "patient_name", Value: nombre},
       },
   }
   msgID, err := birdClient.SendTemplate(phone, tmpl)
   ```

### Canal de templates vs canal regular

El bot usa `BirdChannelIDTemplates` para enviar templates (puede ser diferente al canal de chat `BirdChannelID`). Actualmente ambos apuntan al mismo canal de producción.

---

## 33 — Configuración multi-ambiente (.env / .env.testing)

**Caso:** Quieres agregar una variable que tenga valor diferente en pruebas vs producción, o entender cómo funciona el switching.

### Cómo funciona

```
APP_ENV=testing  → carga .env.testing primero, luego .env como fallback
APP_ENV=production (o vacío) → carga solo .env
```

`godotenv.Load()` **no sobreescribe** variables ya seteadas. Por eso `.env.testing` solo necesita los valores que difieren.

### Archivos involucrados

| Archivo | Contenido |
|---------|-----------|
| `.env` | Valores de **producción** (base) + secretos reales |
| `.env.testing` | Solo overrides para canal de **pruebas** (channel IDs, template IDs, ngrok) |
| `.env.example` | Plantilla con valores de ejemplo (commiteable) |

### Para agregar una variable con valor diferente en testing

1. Agrega el valor de **producción** en `.env`
2. Agrega el override de **pruebas** en `.env.testing`
3. Agrega el ejemplo en `.env.example`
4. El código lo lee normalmente con `os.Getenv("MI_VARIABLE")` — no necesita saber de qué archivo viene

### Ejemplo

```env
# .env (producción):
MI_WEBHOOK_URL=https://api.produccion.com/webhook

# .env.testing (solo si difiere):
MI_WEBHOOK_URL=https://api.pruebas.com/webhook

# .env.example:
MI_WEBHOOK_URL=https://api.example.com/webhook
```

### Archivos en .gitignore

- `.env` → **ignorado** (contiene secretos)
- `.env.testing` → **commiteado** (no contiene secretos, solo IDs de canal)
- `.env.example` → **commiteado** (plantilla)

---

## 34 — Referencia de claves del contexto de sesión

**Caso:** Necesitas saber qué claves de contexto existen, qué significan, y dónde se leen/escriben.

### Claves principales

| Clave | Tipo | Se escribe en | Se lee en | Descripción |
|-------|------|--------------|-----------|-------------|
| `patient_id` | string | identification.go | Múltiples | Documento del paciente |
| `patient_name` | string | identification.go | slots.go, notifications | Nombre completo |
| `patient_doc` | string | identification.go | Múltiples | Número de documento |
| `patient_age` | string (int) | identification.go | medical_validation.go, slots.go | Edad en años |
| `patient_gender` | string | identification.go | medical_validation.go | M o F |
| `patient_entity` | string | entity_management.go | slots.go, notifications | Código de entidad/EPS |
| `menu_option` | string | greeting.go | post_action.go | agendar, consultar, resultados, ubicacion |
| `cups_code` | string | medical_order.go | slots.go, medical_validation.go | Código CUPS del procedimiento |
| `cups_name` | string | medical_order.go | slots.go, notifications | Nombre del procedimiento |
| `cups_preparation` | string | medical_validation.go | slots.go | Instrucciones de preparación |
| `cups_video_url` | string | medical_validation.go | slots.go | URL de video de preparación |
| `cups_audio_url` | string | medical_validation.go | slots.go | URL de audio de preparación |

### Claves de procedimiento y agendamiento

| Clave | Tipo | Descripción |
|-------|------|-------------|
| `is_contrasted` | "0"/"1" | Requiere medio de contraste |
| `is_sedated` | "0"/"1" | Requiere sedación |
| `is_pregnant` | "1" | Paciente embarazada |
| `espacios` | string (int) | Slots consecutivos necesarios |
| `preferred_doctor_doc` | string | Documento del médico preferido (consulta previa) |
| `procedures_json` | string (JSON) | Array JSON de procedimientos de la orden |
| `total_procedures` | string (int) | Total de procedimientos en la orden |
| `current_procedure_idx` | string (int) | Índice del procedimiento actual (0-based) |
| `available_slots_json` | string (JSON) | Resultado de búsqueda de slots |
| `selected_slot_id` | string | ID del slot elegido |
| `slots_after_date` | string (YYYY-MM-DD) | Cursor de paginación para "ver más" |
| `created_appointment_id` | string | ID de la cita creada |

### Claves de GFR (función renal)

| Clave | Tipo | Descripción |
|-------|------|-------------|
| `gfr_creatinine` | string (float) | Valor de creatinina |
| `gfr_height_cm` | string (int) | Altura en cm |
| `gfr_weight_kg` | string (int) | Peso en kg |
| `gfr_disease_type` | string | Clasificación de enfermedad |
| `gfr_calculated` | string (float) | GFR calculado |
| `baby_weight_cat` | string | Categoría de peso del bebé |

### Claves de control interno

| Clave | Tipo | Descripción |
|-------|------|-------------|
| `soat_limit_check` | "1" | Habilita validación de límite mensual CUPS en slot search |
| `reschedule_appt_id` | string | ID de cita a reemplazar (flujo reprogramación) |
| `reschedule_skip_cancel` | "0"/"1" | No cancela la cita vieja al reprogramar |
| `booking_failure_reason` | string | Razón del fallo: "error", "slot_not_found", "slot_taken" |
| `waiting_list_entry_id` | string | ID de entrada de WL (para tracking) |
| `escalation_team` | string | ID del equipo Bird asignado en escalación |
| `pre_escalation_state` | string | Estado antes de escalar (para /bot reanudar) |
| `client_type` | string | Etiqueta del tipo de EPS |
| `ocr_is_sedated` | "1" | OCR detectó que requiere sedación |
| `_prompted_contrast` | "1" | Ya se preguntó sobre contraste |
| `_prompted_sedation` | "1" | Ya se preguntó sobre sedación |
| `_prompted_pregnancy` | "1" | Ya se preguntó sobre embarazo |

> Las claves con prefijo `_` son flags internos de control de flujo — no representan datos del paciente.

---

## 35 — Patrones de testing y mocks

**Caso:** Quieres escribir tests para un handler, servicio o repositorio nuevo.

### Estructura de tests

Los tests siguen el patrón **table-driven** de Go:

```go
func TestMiFuncion(t *testing.T) {
    tests := []struct {
        name     string
        input    string
        wantErr  bool
    }{
        {"caso válido", "abc", false},
        {"caso inválido", "", true},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := MiFuncion(tt.input)
            if (err != nil) != tt.wantErr {
                t.Errorf("MiFuncion(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
            }
        })
    }
}
```

### Mocks de repositorios

Los mocks están en `internal/testutil/mocks.go`. Cada mock usa **funciones como campos** para definir comportamiento por test:

```go
type MockPatientRepo struct {
    FindByDocumentFn func(ctx context.Context, doc string) (*domain.Patient, error)
}

func (m *MockPatientRepo) FindByDocument(ctx context.Context, doc string) (*domain.Patient, error) {
    if m.FindByDocumentFn != nil {
        return m.FindByDocumentFn(ctx, doc)
    }
    return nil, nil // default: no error, no result
}
```

### Para agregar un mock de un repo nuevo

1. Crea la struct en `testutil/mocks.go` con `Fn` fields
2. Implementa cada método de la interfaz delegando al `Fn` correspondiente
3. Si el `Fn` es nil, retorna un valor sensato (nil error, lista vacía, etc.)

### Fixtures de test

`internal/testutil/fixtures.go` contiene datos de prueba reutilizables:

```go
cfg := testutil.SampleConfig()       // Config con valores de test
patient := testutil.SamplePatient()   // Paciente de prueba
appt := testutil.SampleAppointment(time.Now()) // Cita de prueba
```

### Tests de handlers (state machine)

Para testear un handler, usa `sm.NewMachine()` con mocks:

```go
func TestMiHandler(t *testing.T) {
    machine := sm.NewMachine()
    machine.Register(sm.StateMiEstado, miHandler(mockSvc))

    sess := &session.Session{CurrentState: sm.StateMiEstado}
    msg := bird.InboundMessage{Text: "input del usuario"}

    result, err := machine.Process(context.Background(), sess, msg)
    if err != nil {
        t.Fatal(err)
    }
    if result.NextState != sm.StateEsperado {
        t.Errorf("got state %s, want %s", result.NextState, sm.StateEsperado)
    }
}
```

### Para correr tests

```bash
# Todos los tests (Docker, sin Go local):
MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd):/app" -w "/app" golang:1.23-alpine \
  sh -c "go test ./... 2>&1"

# Un paquete específico:
go test ./internal/services/ -run TestCheckSOAT -v
```

---

## 36 — Configuración Docker

**Caso:** Quieres cambiar la configuración de Docker (puertos, recursos, healthchecks, servicios).

### Archivos involucrados

| Archivo | Propósito |
|---------|-----------|
| `docker-compose.yml` | Define servicios (bot + mysql), redes, volúmenes |
| `docker/Dockerfile` | Build del binario Go |
| `docker/mysql/init/` | Scripts SQL de inicialización de MySQL |

### Servicios actuales

| Servicio | Puerto | Recursos (mem/cpu) |
|----------|--------|-------------------|
| `bot` | `${PORT}:${PORT}` | 128M / 1.0 CPU |
| `db` (MySQL) | `3307:3306` | 256M / 1.0 CPU |

### Para cambiar límites de recursos

```yaml
# docker-compose.yml
services:
  bot:
    deploy:
      resources:
        limits:
          memory: 256M  # ← cambiar aquí
          cpus: '2.0'   # ← cambiar aquí
```

### Para agregar un nuevo servicio

```yaml
services:
  # ...servicios existentes...
  redis:
    image: redis:7-alpine
    restart: unless-stopped
    ports:
      - "6379:6379"
```

Y agrega la variable de conexión a `.env` y `config.go`.

### Healthchecks

- **Bot**: `wget --spider http://localhost:${PORT}/health`
- **MySQL**: `mysqladmin ping -h localhost`

### Variables de entorno en Docker

Las variables se inyectan desde `.env` al contenedor vía `env_file: .env` en `docker-compose.yml`. El archivo `.env.testing` se carga **dentro** del código Go (no por Docker).

---

## 37 — Logging y depuración

**Caso:** Quieres agregar logs para depurar un problema o verificar que una operación se ejecutó correctamente.

### Niveles de log y cuándo usar cada uno

El proyecto usa `log/slog` (estándar Go 1.21+). Los niveles son:

| Nivel | Cuándo usar | Visible en producción |
|-------|-------------|----------------------|
| `slog.Debug` | Detalles internos: datos parseados, transiciones, valores intermedios | Solo si `LOG_LEVEL=debug` |
| `slog.Info` | Operaciones completadas: sesión creada, evento procesado, notificación enviada | Sí |
| `slog.Warn` | Situaciones inesperadas no-fatales: firma inválida, rate limit, timeout | Sí |
| `slog.Error` | Errores que impiden una operación: query falló, servicio no disponible | Sí |

### Logs existentes por capa

#### Webhook (entrada de mensajes)

```
webhook_parsed          → mensaje recibido (phone, direction, body_type)
phone not whitelisted   → teléfono rechazado en testing
```

#### Worker Pool (procesamiento)

```
session_state          → estado actual de la sesión (phone, state, retry_count)
processing message     → mensaje en proceso (phone, state, type, text)
state_transition       → transición completada (from, to, messages_count, events_count)
new session created    → sesión nueva (session_id, phone)
event                  → evento de auditoría registrado (type, data)
```

#### State Machine (auto-chain)

```
auto-chaining state    → encadenamiento automático (from, to)
auto-chain cycle guard → ciclo detectado (iterations, states)
```

#### Bird API (WhatsApp)

```
outbound_event_received  → mensaje saliente confirmado (phone, status)
escalate_to_agent_start  → inicio de escalación a agente
agent assigned           → agente asignado en Bird
```

#### Notificaciones

```
whatsapp reminders       → recordatorios enviados (date, count)
wl_check: notification   → lista de espera notificada
confirmation followup    → seguimiento de confirmación enviado
```

### Cómo agregar un log nuevo

```go
import "log/slog"

// En un handler:
slog.Debug("entity_update",
    "patient_id", patientID,
    "entity_code", entityCode,
)

// En un repositorio (error):
slog.Error("update contact info",
    "patient_id", patientID,
    "error", err,
)

// En un servicio (operación completada):
slog.Info("patient_entity_updated",
    "patient_id", patientID,
    "old_entity", oldEntity,
    "new_entity", newEntity,
)
```

### Convenciones

1. **Usar snake_case** para el mensaje principal: `"entity_update"`, no `"Entity Update"`
2. **Pares clave-valor** para datos: `"patient_id", id, "error", err`
3. **No loguear datos sensibles** como contraseñas, tokens o cuerpos completos de requests
4. **Debug para flujo interno**, Info para operaciones de negocio, Warn para degradación, Error para fallas
5. **No duplicar con eventos**: si ya usas `WithEvent("entity_changed", data)`, no necesitas un `slog.Info` con los mismos datos — el worker pool ya loguea todos los eventos automáticamente

### Verificar logs en Docker

```bash
# Logs en tiempo real:
docker compose logs bot -f --tail 50

# Filtrar por nivel:
docker compose logs bot | grep '"level":"ERROR"'

# Filtrar por teléfono:
docker compose logs bot | grep '+573103343616'

# Filtrar por estado:
docker compose logs bot | grep 'ASK_ENTITY_NUMBER'
```

---

## 🔍 Búsquedas útiles con grep

Comandos para encontrar rápidamente lo que necesitas editar:

```powershell
# Encontrar todos los usos de un estado:
grep -r "StateAskDocument" internal/

# Encontrar todos los mensajes que contienen cierto texto:
grep -r "Por favor ingresa" internal/statemachine/handlers/

# Encontrar dónde se registra un handler:
grep -r "Register(" internal/statemachine/handlers/

# Ver todos los estados definidos:
grep "State[A-Z]" internal/statemachine/states.go

# Buscar dónde se lee un valor del contexto de sesión:
grep -r "GetContext(\"cups_code\")" internal/

# Ver todos los eventos de auditoría registrados:
grep -r "WithEvent" internal/statemachine/handlers/

# Ver todos los interceptores registrados:
grep -r "AddInterceptor" internal/

# Encontrar qué estados son automáticos:
grep "StateTypeAutomatic" internal/statemachine/states.go
```

---

## ✅ Checklist universal antes de hacer git push

Antes de hacer `git push`, verifica:

### Compilación y tests
- [ ] El código compila sin errores: `docker run --rm -v "$(pwd):/app" -w "/app" golang:1.23-alpine go build ./cmd/server`
- [ ] Los tests pasan: `docker run --rm -v "$(pwd):/app" -w "/app" golang:1.23-alpine go test ./...`

### State machine
- [ ] Ningún estado nuevo quedó sin registrar en `stateTypes` (states.go)
- [ ] Ningún estado nuevo quedó sin su handler registrado en `RegisterXxxHandlers`
- [ ] Los datos de sesión que se guardan con `WithContext` se leen con `GetContext` en el estado correcto
- [ ] Si cambiaste opciones de botón, el `Options` del `RegisterWithConfig` refleja las mismas opciones que la UI muestra

### Variables de entorno y configuración
- [ ] Si agregaste una variable de entorno, está en `.env.example`
- [ ] Si la variable tiene valor diferente en pruebas, está en `.env.testing`
- [ ] Si agregaste un feature flag, tiene un default sensato en `getEnv("FLAG", "true")`

### Base de datos
- [ ] Si agregaste una migración, el número es secuencial (siguiente al último: 009)
- [ ] Si agregaste un método al repositorio, los mocks en `testutil/mocks.go` están actualizados

### Notificaciones y templates
- [ ] Si agregaste un nuevo tipo de notificación proactiva, se maneja en `HandleResponse` Y en `handleTimeout`
- [ ] Si agregaste postbacks de un template nuevo, están registrados en `isNotificationPostback()`
- [ ] Si agregaste un template de Bird, tiene ProjectID/VersionID/Locale en config y en .env

### API y seguridad
- [ ] Si agregaste un endpoint nuevo, verificaste la firma HMAC si es un webhook de Bird
- [ ] Si el endpoint es interno, está protegido con `InternalAuth` middleware

### Flujos y lógica de negocio
- [ ] Si modificaste un flujo, probaste el camino feliz Y el camino de error (input inválido)
- [ ] Si cambiaste límites SOAT, verificaste que aplican solo a SAN01
- [ ] Si cambiaste enrutamiento de equipos, verificaste con `TeamRoutingEnabled=true` y `false`
- [ ] Si agregaste un interceptor, verificaste que no choca con interceptores existentes

---

*Esta guía cubre 36 escenarios de edición del bot. Si encuentras un caso no cubierto, agrégalo aquí.*
