// Package config lee la configuración del bot del entorno y guarda los valores que se
// consultan al decidir (entre ellos el horario de atención vigente: Hours, schedule.go).
package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	// App
	Port     string
	Timezone string
	LogLevel string
	LogDir   string
	// LogMaxFileMB acota el tamaño de UN archivo de log (LOG_MAX_FILE_MB, 0 = sin tope). Existe para
	// que LOG_LEVEL=debug en producción no sea un riesgo de disco: un bucle caliente escribe MB/s.
	LogMaxFileMB int

	// Local DB
	DBHost     string
	DBPort     string
	DBDatabase string
	DBUser     string
	DBPassword string
	// DBRootPassword: el bot no la usa (es del contenedor MySQL); se lee solo para el aviso de
	// SecurityWarnings.
	DBRootPassword string

	// External DB
	ExtDBHost     string
	ExtDBPort     string
	ExtDBDatabase string
	ExtDBUser     string
	ExtDBPassword string
	ExtDBEncrypt  string // SQL Server "encrypt": disable|true|false (N-11). Prod debería usar "true".

	// External DB Driver — R-ARQ-01
	ExternalDBDriver string // "siesa" (only supported driver; legacy "datosipsndx" removed)

	// SIESA — identidad del usuario que SIESA registra como autor de las citas que crea/cancela/
	// modifica el bot. Son DOS columnas distintas: cod_user_asigna_cita guarda la CÉDULA y
	// usuario_evento/id_usuario_cancela/IdUsuarioConfirmaAsistencia guardan el usuario.id.
	// Usuario PRINCIPAL configurable; si las variables no se definen, cae al usuario de
	// automatización "Procesos Automáticos" (cédula 000000, usuario.id 10006) como FALLBACK.
	SIESAAssignUserCedula string // → cod_user_asigna_cita (cédula). Fallback: "000000"
	SIESAAssignUserID     int    // → usuario_evento / id_usuario_cancela. Fallback: 10006

	// Observabilidad de flujos (docs/OBSERVABILIDAD.md): off|error|outcome|milestone|full.
	FlowTraceLevel string

	// Bird
	BirdAPIURL                     string
	BirdAPIKeyWA                   string
	BirdAccessKeyID                string
	BirdWebhookSecret              string
	BirdWebhookSecretOutbound      string // Separate signing key for outbound webhook (optional)
	BirdWebhookSecretConversations string // Signing key for conversations API webhook (optional, skips validation if empty)
	BirdWebhookSecretVoice         string // Signing key for voice webhook (optional, falls back to BirdWebhookSecret)
	BirdWorkspaceID                string
	BirdChannelID                  string
	BirdTeamGrupoA                 string // Ecografías, RX, Resonancia, TAC
	BirdTeamGrupoB                 string // Neurología, Fisiatría, Estudios del sueño
	BirdTeamFallback               string // Call Center (genérico)
	BirdAgentFallback              string // Líder Call Center — fallback si equipo no disponible

	// Bird Templates
	BirdTemplateConfirmProjectID      string
	BirdTemplateConfirmVersionID      string
	BirdTemplateConfirmLocale         string
	BirdTemplateRescheduleProjectID   string
	BirdTemplateRescheduleVersionID   string
	BirdTemplateRescheduleLocale      string
	BirdTemplateWaitingListProjectID  string
	BirdTemplateWaitingListVersionID  string
	BirdTemplateWaitingListLocale     string
	BirdTemplateCancellationProjectID string
	BirdTemplateCancellationVersionID string
	BirdTemplateCancellationLocale    string

	// Bird Channel for Templates
	BirdChannelIDTemplates string

	// Bird Voice
	BirdAPIKeyVoice    string // AccessKey para el canal de voz (puede diferir del WA key)
	BirdVoiceChannelID string // UUID del canal de voz Bird
	BirdVoiceNumber    string // número "from" para llamadas salientes
	BirdVoiceFlowID    string // UUID del Bird Flow IVR (tiene paso webhook para DTMF result)
	ServerPublicURL    string // URL pública del servidor (para webhooks Bird)

	// Néctar (mensajería WhatsApp que reemplaza a Bird; la voz sigue en Bird). Ninguna de estas
	// variables es obligatoria: vacías, el bot sigue hablando por Bird como hasta hoy.
	// Tres secretos, y no son intercambiables:
	//   - NECTAR_SERVICE_KEY: con la que el bot se identifica en las rutas /api/bot/*.
	//   - NECTAR_NOTICE_SECRET: con la que Néctar FIRMA los avisos que manda al bot
	//     (verifica X-Nectar-*).
	//   - NECTAR_REQUEST_SECRET: con la que el bot FIRMA lo que manda a Néctar.
	NectarBaseURL       string // p. ej. https://nectar.colibrixa.com (sin barra final)
	NectarServiceKey    string
	NectarNoticeSecret  string
	NectarRequestSecret string
	// NectarTemplateNames: "project_id_de_bird=nombre_meta,..." — la correspondencia entre la
	// plantilla de Bird y el nombre que tiene en Meta/Néctar. Vacía: las plantillas por Néctar
	// fallan diciendo qué falta.
	NectarTemplateNames map[string]string
	// NectarTeamByGroup: "imagenes:7,neuro:8,callcenter:9" — el EQUIPO de Néctar de cada grupo de
	// CUPS (ver CupsGroup). Néctar reparte por canal y no sabe qué equipo atiende qué estudio, así
	// que el bot se lo SUGIERE al escalar (SuggestedTeamForCups). Vacía: no se sugiere nada.
	NectarTeamByGroup map[string]int64
	// NectarChannelID (NECTAR_CHANNEL_ID) es el canal de Néctar por el que el bot ABRE conversaciones
	// con plantilla (recordatorios, lista de espera, avisos). 0 = la apertura no está configurada:
	// con el puente puesto, esas plantillas fallan con un error permanente y nada sale.
	NectarChannelID int64
	// LA LÍNEA VIEJA (segundo canal de Néctar en el mismo bot). Todo vacío = apagada y el bot se
	// comporta como siempre. La clave y el canal van juntos (NectarLegacyConfigured); los secretos de
	// ida y de avisos, si no se definen, son los de la línea principal.
	NectarLegacyServiceKey    string // NECTAR_LEGACY_SERVICE_KEY
	NectarLegacyChannelID     int64  // NECTAR_LEGACY_CHANNEL_ID
	NectarLegacyRequestSecret string // NECTAR_LEGACY_REQUEST_SECRET (por defecto NectarRequestSecret)
	NectarLegacyNoticeSecret  string // NECTAR_LEGACY_NOTICE_SECRET (por defecto NectarNoticeSecret)
	// NectarLegacyArmed dice si la línea vieja quedó armada al arrancar. NO sale de una variable:
	// lo pone main, como NectarBridgeArmed.
	NectarLegacyArmed bool
	// EL HORARIO LEÍDO DE NÉCTAR (GET /api/bot/schedule). NectarScheduleEnabled
	// (NECTAR_SCHEDULE_ENABLED) viene APAGADA: apagada rige la semana de siempre
	// (DefaultBusinessHours) y, con puente, se leen solo los festivos de Néctar. El .env la enciende
	// cuando los equipos tengan horario en Néctar. NectarScheduleChannelID (NECTAR_SCHEDULE_CHANNEL_ID) acota la lectura a un canal
	// (0 = todos los de la entidad); NectarScheduleRefreshMin (NECTAR_SCHEDULE_REFRESH_MIN) es cada
	// cuántos minutos se relee (5).
	NectarScheduleEnabled    bool
	NectarScheduleChannelID  int64
	NectarScheduleRefreshMin int
	// NectarReportsSyncMin (NECTAR_REPORTS_SYNC_MIN) es cada cuántos minutos se sincronizan los
	// tramos de espera y el roster de Néctar para los informes de asesores (D5). Solo corre con el
	// puente encendido; 0 o negativo cae a DefaultNectarReportsSyncMin.
	NectarReportsSyncMin int
	// SiesaPendingRetryMin (SIESA_PENDING_RETRY_MIN) es cada cuántos minutos se reintentan en SIESA
	// las respuestas al recordatorio («Confirmar»/«Cancelar») que quedaron pendientes porque SIESA no
	// respondió (D-04); 0 o negativo cae a DefaultSiesaPendingRetryMin.
	SiesaPendingRetryMin int
	// Schedule es el PROVEEDOR del horario vigente (cmd/server lo cablea con un CachedSchedule).
	// Nadie lo lee directo: se pregunta por Hours() — schedule.go.
	Schedule ScheduleSource
	// ProtocolEscaladoVersion (PROTOCOL_ESCALADO_VERSION) es la versión del protocolo de
	// escalado que el bot EMITE: 0 = el dialecto de hoy, 1 = v1. NO gobierna la recepción (dual
	// siempre). Se lee con ProtocolVersion()/EmitsProtocolV1() — protocol.go.
	ProtocolEscaladoVersion int

	// OpenAI
	OpenAIAPIKey string
	OpenAIModel  string
	// OpenAIDocModel es el modelo de las llamadas extra de la orden: la segunda lectura del código CUPS
	// (OCRService.ReadCupsCode, solo cuando el código leído no sirve) y la elección por nombre cuando
	// la orden no trae código (OCRService.ChooseCupsByName).
	OpenAIDocModel string

	// Recuperación asistida por IA (capa antes de escalar; ver docs/RECUPERACION-IA.md).
	// Reutiliza OpenAIAPIKey pero con su PROPIO modelo (fijo en código, distinto al del OCR).
	// El modelo y el max_tokens de salida son constantes internas (recovery.DefaultModel/...).
	AIRecoveryEnabled            bool
	AIRecoveryMaxPatientAttempts int
	AIRecoveryMonthlyLimit       int

	// Bot kill switch: false = escala inmediatamente sin tocar SIESA/Antares
	BotEnabled bool

	// Kill switches de notificaciones proactivas (independientes del bot conversacional):
	// false = no se envían recordatorios/avisos por ese canal (recordatorios diarios, lista de
	// espera, cancelación/reagendamiento admin, cadena de followup).
	WhatsAppNotificationsEnabled bool // WHATSAPP_NOTIFICATIONS_ENABLED — gatea SendTemplate (WA)
	IVRNotificationsEnabled      bool // IVR_NOTIFICATIONS_ENABLED — gatea PlaceCall (llamadas IVR)
	// SAME_DAY_REMINDERS_ENABLED — recordatorio para citas de corta antelación (agendadas después de la
	// corrida de las 07:00, que nunca reciben el recordatorio del día antes; 25,6% de no-show medido).
	// Requiere también WhatsAppNotificationsEnabled. Apagarlo NO afecta el flujo de las 07:00.
	SameDayRemindersEnabled bool
	// MORNING_REENGAGE_ENABLED - re-enganche 07:05 a quienes rebotaron fuera de horario ayer >=17h (par.8.1 #9).
	MorningReengageEnabled bool

	// Session
	SessionTimeoutMinutes int

	// Inactivity (minutes without patient response)
	InactivityReminderMin int // Single reminder
	InactivityCloseMin    int // Silent close (no message)

	// Escalation (chat con agente humano)
	EscalationPatientCloseMin int // Cierre escalada por silencio del PACIENTE (no del agente)
	// EscalationNectarCloseMin (ESCALATION_NECTAR_CLOSE_MIN) es el cierre por silencio de las escalaciones
	// de Néctar: red de seguridad, el fin normal es conversacion_resuelta. Ver parseNectarCloseMin.
	EscalationNectarCloseMin   int
	EscalationAgentReminderMin int // Minutos sin respuesta del agente antes de recordarle
	// EscalationAgentReminderMax: máximo de recordatorios. La ventana total del agente antes de
	// devolver el chat al bot = AgentReminderMin*(AgentReminderMax+1). Con 75 y 3 → 5h (recordatorios
	// a 75/150/225 min, devolución a los 300).
	EscalationAgentReminderMax int

	// Center
	CenterKey  string
	CenterName string
	// MedicationExternalWANumber: número WhatsApp (internacional, solo dígitos, ej. 573001234567) del
	// canal externo de "Aplicación de medicamentos". Si está definido, el bot envía al paciente el link
	// wa.me a ese canal (no escala); si está vacío, escala a un agente como antes.
	MedicationExternalWANumber string
	BotName                    string
	ResultsURL                 string
	ResultsVideoURL            string
	// Resultados de estudios (API de resultados, PDF por Néctar, portal de imágenes). Todo vacío o
	// false = «Resultados» se comporta como hoy.
	ResultsAPIURL     string // RESULTS_API_URL: base de la API de resultados, sin barra final. Vacía = apagada.
	ResultsAPIKey     string // RESULTS_API_KEY: opcional.
	ResultsPDFEnabled bool   // RESULTS_PDF_ENABLED=true: adjuntos PDF por Néctar. Por defecto false.
	ImagingResultsURL string // IMAGING_RESULTS_URL: enlace al portal de imágenes. Vacía = la opción no aparece.

	// Security
	InternalAPIKey string
	LogMaskPhones  bool // Mask phone numbers in logs (default true; set LOG_MASK_PHONES=false to disable)

	// TrustedProxyHosts son los nombres (DNS de Docker) del proxy delante del bot: solo a las IPs que
	// resuelven, más loopback, se les cree el X-Forwarded-For para el límite de la API interna. Hoy es un
	// solo proxy, el servicio ngrok del compose. Sin definir = "ngrok"; definida vacía = no se cree a nadie.
	TrustedProxyHosts []string

	// Ngrok
	NgrokHostname string

	// Testing
	TestingAlwaysOpen bool // Bypasses business hours check when true
	MaxRetries        int  // Max invalid response attempts before fallback menu

	// BusinessHoursCloseMarginMin (BUSINESS_HOURS_CLOSE_MARGIN_MIN) son los minutos antes
	// del cierre a partir de los cuales una escalación ya NO la alcanza a atender una
	// persona hoy y se encola para el próximo día hábil. El BOT sigue atendiendo hasta el
	// cierre exacto: esto solo gobierna lo que depende de un asesor.
	//
	// El valor CERO —un Config armado a mano en pruebas— apaga la ventana del margen, y
	// entonces solo encola lo que llegue con el horario ya cerrado. El default de 15 minutos
	// lo pone Load().
	BusinessHoursCloseMarginMin int

	// BusinessHoursQueueMaxWaitMin (BUSINESS_HOURS_QUEUE_MAX_WAIT_MIN) es lo máximo que puede
	// faltar para la próxima apertura para que encolar el caso siga teniendo sentido. Con la
	// apertura MÁS lejos (un fin de semana) la respuesta del asesor caería fuera de la ventana
	// de 24 h de WhatsApp, así que se vuelve al desvío de siempre: «escríbenos en horario de
	// atención».
	//
	// El valor CERO —un Config armado a mano en pruebas— apaga la cola entera, que es el
	// comportamiento conservador. El default de 20 h lo pone Load().
	BusinessHoursQueueMaxWaitMin int

	// Tope de envíos salientes por teléfono/hora SIN inbound del paciente (0 = off).
	// Última defensa contra emisores desbocados (incidente 11/12-ago-2026: 1 msg/min toda la noche).
	SendRateLimitPerHour int

	// CUPS group limits
	CupsGroupLimitsEnabled bool // Monthly CUPS group limits for Sanitas MRC (contracts 5/6)
	TeamRoutingEnabled     bool // Route to specialty teams (Grupo A/B); false → all to Call Center

	// Confirmation escalation chain
	ConfirmFollowupEnabled bool // Enable follow-up messages + IVR after initial reminder (default false)
	ConfirmFollowup1Hours  int  // First follow-up after WA template (hours)
	ConfirmFollowup2Hours  int  // Second follow-up after first (hours)
	ConfirmPostIVRMinutes  int  // Agent escalation after IVR (minutes)

	// Telegram error alerts (optional — empty means disabled)
	TelegramBotToken string
	TelegramChatID   string

	// Aviso URGENTE de volver a Bird (internal/monitor/urgent.go): solo con Telegram y el puente de
	// Néctar encendido. Enteros positivos (parsePositiveInt); un valor ilegible tumba el arranque.
	UrgentAlertCooldownMin        int // URGENT_ALERT_COOLDOWN_MIN: minutos entre avisos del mismo disparador
	UrgentUnsentThreshold         int // URGENT_UNSENT_THRESHOLD: recordatorios sin enviar por corrida que avisan
	UrgentNectarDownCount         int // URGENT_NECTAR_DOWN_COUNT: fallos de transporte/5xx de Néctar que avisan…
	UrgentNectarDownWindowMin     int // URGENT_NECTAR_DOWN_WINDOW_MIN: …dentro de esta ventana (min)
	UrgentEscalationFailCount     int // URGENT_ESCALATION_FAIL_COUNT: escalaciones a Néctar fallidas que avisan…
	UrgentEscalationFailWindowMin int // URGENT_ESCALATION_FAIL_WINDOW_MIN: …dentro de esta ventana (min)

	// Testing whitelist — only these phones can interact with the bot (empty = all allowed)
	TestingWhitelistPhones []string

	// NewLineAnnouncement (NEW_LINE_ANNOUNCEMENT): mientras conviven las dos líneas, el texto con
	// el que los saludos de la línea de Bird anuncian la nueva. Vacía = apagado (saludo de siempre).
	// Solo se anuncia con NectarBridgeArmed: sin puente no hay línea nueva a la que mandar a nadie.
	NewLineAnnouncement string
	// NectarBridgeArmed dice si el puente de Néctar quedó armado al arrancar. NO sale de una variable
	// de entorno: cmd/server lo pone cuando nectarBridgeFromConfig arma el puente (como Schedule).
	// La vuelta a Bird (NECTAR_BASE_URL vacía) lo deja en false y apaga con él el anuncio.
	NectarBridgeArmed bool
	// AgentsSource (AGENTS_SOURCE) dice dónde viven los agentes que lista el informe del
	// dashboard: "bird" (default) o "nectar". NO se deduce del puente: en la transición el puente
	// está encendido y la mayoría de agentes sigue en Bird. Se lee con AgentsInNectar().
	AgentsSource string

	// Scaling — configurable pool sizes and DB connections for load profiles
	ScalingProfile    string // "normal" or "high-load" (informational, for monitoring alerts)
	WorkerPoolSize    int    // Worker goroutines (default 10)
	WorkerQueueSize   int    // Message queue buffer (default 100)
	LocalDBMaxOpen    int    // Local DB max open connections (default 25)
	LocalDBMaxIdle    int    // Local DB max idle connections (default 10)
	ExternalDBMaxOpen int    // External DB max open connections (default 10)
	// ExternalDBKPIMaxOpen: pool SIESA DEDICADO a los KPIs/analytics del dashboard (P5). Separado
	// del pool principal para que una ráfaga de KPIs (8 queries al cargar la vista SIESA en frío)
	// no deje sin conexiones al flujo de agendamiento de pacientes.
	ExternalDBKPIMaxOpen int // default 3
	ExternalDBMaxIdle    int // External DB max idle connections (default 5)
	HTTPReadTimeout      int // HTTP server read timeout seconds (default 30)
	HTTPWriteTimeout     int // HTTP server write timeout seconds (default 30)
	HTTPIdleTimeout      int // HTTP server idle timeout seconds (default 60)
	MySQLMaxConns        int // MySQL max_connections hint for docker-compose (informational)
}

func Load() *Config {
	godotenv.Load() // loads .env

	cfg := &Config{
		// App
		Port:     getEnv("PORT", "8080"),
		Timezone: getEnv("TZ", "America/Bogota"),
		LogLevel: getEnv("LOG_LEVEL", "info"),
		LogDir:   getEnv("LOG_DIR", "/app/logs"),
		// 512 MB: muy por encima de un día normal (~30 MB), y muy por debajo de lo que un bucle
		// desbocado escribe en una hora (~10 GB).
		LogMaxFileMB: getEnvInt("LOG_MAX_FILE_MB", 512),

		// Local DB
		DBHost:         getEnv("DB_HOST", "db"),
		DBPort:         getEnv("DB_PORT", "3306"),
		DBDatabase:     getEnv("DB_DATABASE", "neuro_bot"),
		DBUser:         getEnv("DB_USER", "botuser"),
		DBPassword:     os.Getenv("DB_PASSWORD"),
		DBRootPassword: os.Getenv("DB_ROOT_PASSWORD"),

		// External DB
		ExtDBHost:     getEnv("EXTERNAL_DB_HOST", "host.docker.internal"),
		ExtDBPort:     getEnv("EXTERNAL_DB_PORT", "1433"),
		ExtDBDatabase: getEnv("EXTERNAL_DB_DATABASE", "ZeusSalud_Neuro"),
		ExtDBUser:     os.Getenv("EXTERNAL_DB_USER"),
		ExtDBPassword: os.Getenv("EXTERNAL_DB_PASSWORD"),
		// Default "disable" preserva el comportamiento actual (no rompe el server de pruebas);
		// en producción setear EXTERNAL_DB_ENCRYPT=true para cifrar el canal TDS (PII de salud).
		ExtDBEncrypt: getEnv("EXTERNAL_DB_ENCRYPT", "disable"),

		// SIESA — usuario autor de las citas (principal SHERNANDEZ en prod; fallback automatización).
		SIESAAssignUserCedula: getEnv("SIESA_ASSIGN_USER_CEDULA", "000000"),
		SIESAAssignUserID:     getEnvInt("SIESA_ASSIGN_USER_ID", 10006),

		// External DB Driver
		ExternalDBDriver: getEnv("EXTERNAL_DB_DRIVER", "siesa"),

		// Observabilidad de flujos (default milestone = recorrido de negocio consultable).
		FlowTraceLevel: getEnv("FLOW_TRACE_LEVEL", "milestone"),

		// Bird
		BirdAPIURL:                     os.Getenv("BIRD_API_URL"),
		BirdAPIKeyWA:                   os.Getenv("BIRD_API_KEY_WA"),
		BirdAccessKeyID:                os.Getenv("BIRD_ACCESS_KEY_ID"),
		BirdWebhookSecret:              os.Getenv("BIRD_WEBHOOK_SECRET"),
		BirdWebhookSecretOutbound:      os.Getenv("BIRD_WEBHOOK_SECRET_OUTBOUND"),
		BirdWebhookSecretConversations: os.Getenv("BIRD_WEBHOOK_SECRET_CONVERSATIONS"),
		BirdWebhookSecretVoice:         os.Getenv("BIRD_WEBHOOK_SECRET_VOICE"),
		BirdWorkspaceID:                os.Getenv("BIRD_WORKSPACE_ID"),
		BirdChannelID:                  os.Getenv("BIRD_CHANNEL_ID"),
		BirdTeamGrupoA:                 os.Getenv("BIRD_TEAM_GRUPO_A"),
		BirdTeamGrupoB:                 os.Getenv("BIRD_TEAM_GRUPO_B"),
		BirdTeamFallback:               os.Getenv("BIRD_TEAM_FALLBACK"),
		BirdAgentFallback:              os.Getenv("BIRD_AGENT_FALLBACK"),

		// Bird Templates
		BirdTemplateConfirmProjectID:      os.Getenv("BIRD_TEMPLATE_CONFIRM_PROJECT_ID"),
		BirdTemplateConfirmVersionID:      os.Getenv("BIRD_TEMPLATE_CONFIRM_VERSION_ID"),
		BirdTemplateConfirmLocale:         getEnv("BIRD_TEMPLATE_CONFIRM_LOCALE", "es-MX"),
		BirdTemplateRescheduleProjectID:   os.Getenv("BIRD_TEMPLATE_RESCHEDULE_PROJECT_ID"),
		BirdTemplateRescheduleVersionID:   os.Getenv("BIRD_TEMPLATE_RESCHEDULE_VERSION_ID"),
		BirdTemplateRescheduleLocale:      getEnv("BIRD_TEMPLATE_RESCHEDULE_LOCALE", "es-CO"),
		BirdTemplateWaitingListProjectID:  os.Getenv("BIRD_TEMPLATE_WAITING_LIST_PROJECT_ID"),
		BirdTemplateWaitingListVersionID:  os.Getenv("BIRD_TEMPLATE_WAITING_LIST_VERSION_ID"),
		BirdTemplateWaitingListLocale:     getEnv("BIRD_TEMPLATE_WAITING_LIST_LOCALE", "es-CO"),
		BirdTemplateCancellationProjectID: os.Getenv("BIRD_TEMPLATE_CANCELLATION_PROJECT_ID"),
		BirdTemplateCancellationVersionID: os.Getenv("BIRD_TEMPLATE_CANCELLATION_VERSION_ID"),
		BirdTemplateCancellationLocale:    getEnv("BIRD_TEMPLATE_CANCELLATION_LOCALE", "es-CO"),

		// Bird Channel for Templates
		BirdChannelIDTemplates: os.Getenv("BIRD_CHANNEL_ID_TEMPLATES"),

		// Bird Voice
		BirdAPIKeyVoice:    os.Getenv("BIRD_API_KEY_VOICE"),
		BirdVoiceChannelID: os.Getenv("BIRD_VOICE_CHANNEL_ID"),
		BirdVoiceNumber:    os.Getenv("BIRD_VOICE_NUMBER"),
		BirdVoiceFlowID:    os.Getenv("BIRD_VOICE_FLOW_ID"),
		ServerPublicURL:    os.Getenv("SERVER_PUBLIC_URL"),

		// Néctar
		NectarBaseURL:       strings.TrimRight(os.Getenv("NECTAR_BASE_URL"), "/"),
		NectarServiceKey:    os.Getenv("NECTAR_SERVICE_KEY"),
		NectarNoticeSecret:  os.Getenv("NECTAR_NOTICE_SECRET"),
		NectarRequestSecret: os.Getenv("NECTAR_REQUEST_SECRET"),
		NectarTemplateNames: parseKeyValueList(os.Getenv("NECTAR_TEMPLATE_NAMES")),

		// OpenAI
		OpenAIAPIKey:   os.Getenv("OPENAI_API_KEY"),
		OpenAIModel:    getEnv("OPENAI_MODEL", "gpt-4o-mini"),
		OpenAIDocModel: getEnv("OPENAI_DOC_MODEL", "gpt-4.1"),

		// Recuperación asistida por IA (3 env; modelo y max_tokens fijos en código)
		AIRecoveryEnabled:            getEnvBool("AI_RECOVERY_ENABLED", true),
		AIRecoveryMaxPatientAttempts: getEnvInt("AI_RECOVERY_MAX_PATIENT_ATTEMPTS", 2),
		AIRecoveryMonthlyLimit:       getEnvInt("AI_RECOVERY_MONTHLY_LIMIT", 0),

		// Tope anti-emisor-desbocado (send_rate_limit.go). 30/h por teléfono: una conversación real
		// nunca lo toca (cada inbound del paciente resetea la cuota); un bucle sin paciente sí.
		SendRateLimitPerHour: getEnvInt("SEND_RATE_LIMIT_PER_HOUR", 30),

		// Bot kill switch
		BotEnabled:                   getEnvBool("BOT_ENABLED", true),
		WhatsAppNotificationsEnabled: getEnvBool("WHATSAPP_NOTIFICATIONS_ENABLED", true),
		IVRNotificationsEnabled:      getEnvBool("IVR_NOTIFICATIONS_ENABLED", true),
		SameDayRemindersEnabled:      getEnvBool("SAME_DAY_REMINDERS_ENABLED", true),
		MorningReengageEnabled:       getEnvBool("MORNING_REENGAGE_ENABLED", true),

		// Session
		SessionTimeoutMinutes: getEnvInt("SESSION_TIMEOUT_MINUTES", 120),

		// Inactivity
		InactivityReminderMin: getEnvInt("INACTIVITY_REMINDER_MIN", 20),
		InactivityCloseMin:    getEnvInt("INACTIVITY_CLOSE_MIN", 120),

		// Escalation
		EscalationPatientCloseMin:  getEnvInt("ESCALATION_PATIENT_CLOSE_MIN", 120),
		EscalationAgentReminderMin: getEnvInt("ESCALATION_AGENT_REMINDER_MIN", 75), // 75*(3+1)=300min=5h de ventana del agente
		EscalationAgentReminderMax: getEnvInt("ESCALATION_AGENT_REMINDER_MAX", 3),

		// Center
		CenterKey:                  getEnv("CENTER_KEY", "siesa"),
		CenterName:                 getEnv("CENTER_NAME", "Neuro Electrodiagnóstico del Llano"),
		MedicationExternalWANumber: digitsOnly(getEnv("MEDICATION_EXTERNAL_WA_NUMBER", "")),
		BotName:                    getEnv("BOT_NAME", "Samuel"),
		ResultsURL:                 getEnv("RESULTS_URL", ""),
		ResultsVideoURL:            getEnv("RESULTS_VIDEO_URL", ""),
		ResultsAPIURL:              strings.TrimRight(strings.TrimSpace(os.Getenv("RESULTS_API_URL")), "/"),
		ResultsAPIKey:              strings.TrimSpace(os.Getenv("RESULTS_API_KEY")),
		ResultsPDFEnabled:          getEnvBool("RESULTS_PDF_ENABLED", false),
		ImagingResultsURL:          strings.TrimSpace(os.Getenv("IMAGING_RESULTS_URL")),

		// Security
		InternalAPIKey: os.Getenv("INTERNAL_API_KEY"),
		// #31 (auditoría): getEnvBool (acepta true/1/false/0, case-insensitive) en vez de == "true"
		// estricto — un "True"/"1" desactivaba el masking de teléfonos (PII) silenciosamente.
		LogMaskPhones:     getEnvBool("LOG_MASK_PHONES", true),
		TrustedProxyHosts: trustedProxyHosts(),

		// Ngrok
		NgrokHostname: os.Getenv("NGROK_HOSTNAME"),

		// Testing
		TestingAlwaysOpen: getEnvBool("TESTING_ALWAYS_OPEN", false),
		MaxRetries:        getEnvInt("MAX_RETRIES", 4),

		// CUPS group limits
		CupsGroupLimitsEnabled: getEnvBool("CUPS_GROUP_LIMITS_ENABLED", true),
		TeamRoutingEnabled:     getEnvBool("TEAM_ROUTING_ENABLED", true),

		// Confirmation escalation
		ConfirmFollowupEnabled: getEnvBool("CONFIRMATION_FOLLOWUP_ENABLED", false),
		ConfirmFollowup1Hours:  getEnvInt("CONFIRMATION_FOLLOWUP_1_HOURS", 3),
		ConfirmFollowup2Hours:  getEnvInt("CONFIRMATION_FOLLOWUP_2_HOURS", 3),
		ConfirmPostIVRMinutes:  getEnvInt("CONFIRMATION_POST_IVR_MINUTES", 30),

		// Telegram error alerts
		TelegramBotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:   os.Getenv("TELEGRAM_CHAT_ID"),

		// Testing whitelist
		TestingWhitelistPhones: parseCSVList(os.Getenv("TESTING_WHITELIST_PHONES")),

		// Transición de líneas
		NewLineAnnouncement: strings.TrimSpace(os.Getenv("NEW_LINE_ANNOUNCEMENT")),

		// Scaling
		ScalingProfile:       getEnv("SCALING_PROFILE", "normal"),
		WorkerPoolSize:       getEnvInt("WORKER_POOL_SIZE", 10),
		WorkerQueueSize:      getEnvInt("WORKER_QUEUE_SIZE", 100),
		LocalDBMaxOpen:       getEnvInt("LOCAL_DB_MAX_OPEN", 25),
		LocalDBMaxIdle:       getEnvInt("LOCAL_DB_MAX_IDLE", 10),
		ExternalDBMaxOpen:    getEnvInt("EXTERNAL_DB_MAX_OPEN", 10),
		ExternalDBMaxIdle:    getEnvInt("EXTERNAL_DB_MAX_IDLE", 5),
		ExternalDBKPIMaxOpen: getEnvInt("EXTERNAL_DB_KPI_MAX_OPEN", 3),
		HTTPReadTimeout:      getEnvInt("HTTP_READ_TIMEOUT", 30),
		HTTPWriteTimeout:     getEnvInt("HTTP_WRITE_TIMEOUT", 30),
		HTTPIdleTimeout:      getEnvInt("HTTP_IDLE_TIMEOUT", 60),
	}

	// La versión del protocolo de escalado y el equipo de Néctar por grupo de CUPS NO se leen con
	// getEnvInt: un valor que no se entendió tiene que verse al arrancar. Interpretarlo como
	// «0 por si acaso» dejaría al bot hablando el dialecto de hoy creyendo que habla v1, y un par
	// de equipos mal escrito dejaría de sugerir equipo sin que nadie lo note.
	protoVer, err := parseProtocolEscaladoVersion(os.Getenv("PROTOCOL_ESCALADO_VERSION"))
	if err != nil {
		log.Fatalf("PROTOCOL_ESCALADO_VERSION inválida: %v", err)
	}
	cfg.ProtocolEscaladoVersion = protoVer

	agentsSource, err := parseAgentsSource(os.Getenv("AGENTS_SOURCE"))
	if err != nil {
		log.Fatalf("AGENTS_SOURCE inválida: %v", err)
	}
	cfg.AgentsSource = agentsSource

	teams, err := parseNectarTeamByGroup(os.Getenv("NECTAR_TEAM_BY_GROUP"))
	if err != nil {
		log.Fatalf("NECTAR_TEAM_BY_GROUP inválida: %v", err)
	}
	cfg.NectarTeamByGroup = teams

	channelID, err := parseNectarChannelID(os.Getenv("NECTAR_CHANNEL_ID"))
	if err != nil {
		log.Fatalf("NECTAR_CHANNEL_ID inválida: %v", err)
	}
	cfg.NectarChannelID = channelID

	legacyChannelID, err := parseNectarChannelID(os.Getenv("NECTAR_LEGACY_CHANNEL_ID"))
	if err != nil {
		log.Fatalf("NECTAR_LEGACY_CHANNEL_ID inválida: %v", err)
	}
	cfg.NectarLegacyChannelID = legacyChannelID
	cfg.NectarLegacyServiceKey = strings.TrimSpace(os.Getenv("NECTAR_LEGACY_SERVICE_KEY"))
	cfg.NectarLegacyRequestSecret = getEnv("NECTAR_LEGACY_REQUEST_SECRET", cfg.NectarRequestSecret)
	cfg.NectarLegacyNoticeSecret = getEnv("NECTAR_LEGACY_NOTICE_SECRET", cfg.NectarNoticeSecret)

	cfg.NectarScheduleEnabled = getEnvBool("NECTAR_SCHEDULE_ENABLED", false)
	cfg.NectarScheduleRefreshMin = getEnvInt("NECTAR_SCHEDULE_REFRESH_MIN", DefaultScheduleRefreshMin)
	cfg.NectarReportsSyncMin = getEnvInt("NECTAR_REPORTS_SYNC_MIN", DefaultNectarReportsSyncMin)
	if cfg.NectarReportsSyncMin <= 0 {
		// 0 no es «no sincronizar»: sería un bucle sin pausa contra Néctar.
		cfg.NectarReportsSyncMin = DefaultNectarReportsSyncMin
	}
	cfg.SiesaPendingRetryMin = getEnvInt("SIESA_PENDING_RETRY_MIN", DefaultSiesaPendingRetryMin)
	if cfg.SiesaPendingRetryMin <= 0 {
		cfg.SiesaPendingRetryMin = DefaultSiesaPendingRetryMin
	}
	scheduleChannelID, err := parseNectarChannelID(os.Getenv("NECTAR_SCHEDULE_CHANNEL_ID"))
	if err != nil {
		log.Fatalf("NECTAR_SCHEDULE_CHANNEL_ID inválida: %v", err)
	}
	cfg.NectarScheduleChannelID = scheduleChannelID

	// El margen de cierre y la espera máxima de la cola, por el mismo camino: un valor
	// imposible tiene que verse al arrancar y no el día que alguien pregunte por qué todos los
	// casos quedaron para mañana (o por qué ninguno).
	margin, err := parseCloseMarginMin(os.Getenv("BUSINESS_HOURS_CLOSE_MARGIN_MIN"))
	if err != nil {
		log.Fatalf("BUSINESS_HOURS_CLOSE_MARGIN_MIN inválido: %v", err)
	}
	cfg.BusinessHoursCloseMarginMin = margin

	maxWait, err := parseQueueMaxWaitMin(os.Getenv("BUSINESS_HOURS_QUEUE_MAX_WAIT_MIN"))
	if err != nil {
		log.Fatalf("BUSINESS_HOURS_QUEUE_MAX_WAIT_MIN inválido: %v", err)
	}
	cfg.BusinessHoursQueueMaxWaitMin = maxWait

	nectarClose, err := parseNectarCloseMin(os.Getenv("ESCALATION_NECTAR_CLOSE_MIN"))
	if err != nil {
		log.Fatalf("ESCALATION_NECTAR_CLOSE_MIN inválido: %v", err)
	}
	cfg.EscalationNectarCloseMin = nectarClose

	// Umbrales del aviso urgente de volver a Bird: mismo criterio, un valor ilegible se ve al arrancar.
	for _, u := range []struct {
		key  string
		def  int
		dest *int
	}{
		{"URGENT_ALERT_COOLDOWN_MIN", 10, &cfg.UrgentAlertCooldownMin},
		{"URGENT_UNSENT_THRESHOLD", 10, &cfg.UrgentUnsentThreshold},
		{"URGENT_NECTAR_DOWN_COUNT", 5, &cfg.UrgentNectarDownCount},
		{"URGENT_NECTAR_DOWN_WINDOW_MIN", 5, &cfg.UrgentNectarDownWindowMin},
		{"URGENT_ESCALATION_FAIL_COUNT", 3, &cfg.UrgentEscalationFailCount},
		{"URGENT_ESCALATION_FAIL_WINDOW_MIN", 10, &cfg.UrgentEscalationFailWindowMin},
	} {
		v, err := parsePositiveInt(os.Getenv(u.key), u.def)
		if err != nil {
			log.Fatalf("%s inválido: %v", u.key, err)
		}
		*u.dest = v
	}

	cfg.validate()
	return cfg
}

func (c *Config) validate() {
	required := map[string]string{
		"DB_HOST":              c.DBHost,
		"DB_PASSWORD":          c.DBPassword,
		"EXTERNAL_DB_USER":     c.ExtDBUser,
		"EXTERNAL_DB_PASSWORD": c.ExtDBPassword,
		"BIRD_API_URL":         c.BirdAPIURL,
		"BIRD_API_KEY_WA":      c.BirdAPIKeyWA,
		"BIRD_WEBHOOK_SECRET":  c.BirdWebhookSecret,
		"BIRD_WORKSPACE_ID":    c.BirdWorkspaceID,
		"BIRD_CHANNEL_ID":      c.BirdChannelID,
		"BIRD_TEAM_FALLBACK":   c.BirdTeamFallback,
		"OPENAI_API_KEY":       c.OpenAIAPIKey,
		"INTERNAL_API_KEY":     c.InternalAPIKey,
	}

	var missing []string
	for name, value := range required {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		log.Fatalf("Missing required env vars: %s", strings.Join(missing, ", "))
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// digitsOnly conserva solo los dígitos de s. Para el número de WhatsApp del canal externo: acepta que
// lo escriban con "+", espacios o guiones (ej. "+57 300 123 4567") y lo deja como "573001234567", que
// es el formato que exige wa.me (indicativo + número, sin "+").
func digitsOnly(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

// trustedProxyHosts lee TRUSTED_PROXY_HOSTS distinguiendo sin definir (el servicio ngrok del compose)
// de definida vacía (no se confía en ningún proxy).
func trustedProxyHosts() []string {
	raw, ok := os.LookupEnv("TRUSTED_PROXY_HOSTS")
	if !ok {
		return []string{"ngrok"}
	}
	return parseCSVList(raw)
}

// parseCSVList parte una lista separada por comas, recortando espacios y descartando entradas vacías.
func parseCSVList(s string) []string {
	if s == "" {
		return nil
	}
	var phones []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			phones = append(phones, p)
		}
	}
	return phones
}

// IsPhoneWhitelisted returns true if the phone is allowed to interact with the bot.
// When the whitelist is empty, all phones are allowed.
func (c *Config) IsPhoneWhitelisted(phone string) bool {
	if len(c.TestingWhitelistPhones) == 0 {
		return true
	}
	for _, p := range c.TestingWhitelistPhones {
		if p == phone {
			return true
		}
	}
	return false
}

// Grupos de CUPS que deciden a qué equipo se escala. Los valores son las claves de
// NECTAR_TEAM_BY_GROUP.
const (
	CupsGroupImaging    = "imagenes"   // Ecografía, RX, Resonancia, TAC (Grupo A)
	CupsGroupNeuro      = "neuro"      // EMG / Fisiatría, Neurología (Grupo B)
	CupsGroupCallCenter = "callcenter" // todo lo demás (genérico)
)

// NectarGroupSIAU es la clave de NECTAR_TEAM_BY_GROUP del equipo de Atención al usuario (SIAU). No es
// un grupo de CUPS: CupsGroup nunca lo devuelve, así que SuggestedTeamForCups no lo usa.
const NectarGroupSIAU = "siau"

// CupsGroup devuelve el grupo de CUPS de un código de procedimiento: CupsGroupImaging,
// CupsGroupNeuro o CupsGroupCallCenter (lo desconocido y lo demasiado corto). Es la única tabla
// de prefijos: de ella cuelgan tanto ResolveTeamForCups (equipo de Bird) como
// SuggestedTeamForCups (equipo de Néctar).
func (*Config) CupsGroup(cupsCode string) string {
	if len(cupsCode) < 3 {
		return CupsGroupCallCenter
	}
	p3 := cupsCode[:3]
	switch {
	case p3 == "881" || p3 == "882": // Ecografía
		return CupsGroupImaging
	case p3 == "883": // Resonancia Magnética
		return CupsGroupImaging
	case p3 == "871" || p3 == "879": // Tomografía (TAC)
		return CupsGroupImaging
	case p3 == "870" || (p3 >= "873" && p3 <= "878"): // Rayos X
		return CupsGroupImaging
	case p3 == "291" || p3 == "930" || p3 == "892": // EMG / Fisiatría
		return CupsGroupNeuro
	case cupsCode == "890274" || cupsCode == "890374" || cupsCode == "053105": // Neurología
		return CupsGroupNeuro
	default:
		return CupsGroupCallCenter
	}
}

// ResolveTeamForCups returns the Bird team ID based on the CUPS procedure code.
// Falls back to BirdTeamFallback (Call Center) for unknown codes.
// When TeamRoutingEnabled is false, always returns Call Center.
func (c *Config) ResolveTeamForCups(cupsCode string) string {
	if !c.TeamRoutingEnabled {
		return c.BirdTeamFallback
	}
	switch c.CupsGroup(cupsCode) {
	case CupsGroupImaging:
		return c.BirdTeamGrupoA
	case CupsGroupNeuro:
		return c.BirdTeamGrupoB
	default:
		return c.BirdTeamFallback
	}
}

// SuggestedTeamForCups devuelve el equipo de Néctar del grupo de ese CUPS, o nil si no hay
// mapa configurado o el grupo no tiene id (el estado de hoy, con la variable vacía: el escalado
// no sugiere equipo). Devuelve una copia: quien la reciba no altera el mapa de la config.
func (c *Config) SuggestedTeamForCups(cupsCode string) *int64 {
	if c == nil || len(c.NectarTeamByGroup) == 0 {
		return nil
	}
	team, ok := c.NectarTeamByGroup[c.CupsGroup(cupsCode)]
	if !ok {
		return nil
	}
	return &team
}

// SuggestedTeamForSIAU devuelve el equipo de Néctar de SIAU (clave siau de NECTAR_TEAM_BY_GROUP), o nil
// si el mapa no trae esa clave. Devuelve una copia, como SuggestedTeamForCups.
func (c *Config) SuggestedTeamForSIAU() *int64 {
	if c == nil {
		return nil
	}
	team, ok := c.NectarTeamByGroup[NectarGroupSIAU]
	if !ok {
		return nil
	}
	return &team
}

// parseNectarTeamByGroup lee "imagenes:7, neuro:8, callcenter:9, siau:12" en un mapa grupo → id de
// equipo de Néctar. Vacía (o solo espacios) da un mapa vacío. Las claves válidas son los grupos de
// CupsGroup y NectarGroupSIAU; todo lo que no se entienda (grupo desconocido o repetido, par sin ':',
// id no numérico o <= 0) es ERROR de arranque, porque tragarse un par malo dejaría ese grupo
// escalando sin equipo sugerido y nadie se enteraría. El grupo se normaliza a minúsculas y se
// toleran espacios alrededor de cada pieza.
func parseNectarTeamByGroup(raw string) (map[string]int64, error) {
	out := map[string]int64{}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		name, id, ok := strings.Cut(item, ":")
		if !ok {
			return nil, fmt.Errorf("par %q sin ':' (forma: grupo:id_de_equipo)", item)
		}
		name = strings.ToLower(strings.TrimSpace(name))
		switch name {
		case CupsGroupImaging, CupsGroupNeuro, CupsGroupCallCenter, NectarGroupSIAU:
		default:
			return nil, fmt.Errorf("grupo %q desconocido (válidos: %s, %s, %s, %s)",
				name, CupsGroupImaging, CupsGroupNeuro, CupsGroupCallCenter, NectarGroupSIAU)
		}
		team, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("grupo %q: id de equipo %q no numérico", name, strings.TrimSpace(id))
		}
		if team <= 0 {
			return nil, fmt.Errorf("grupo %q: id de equipo %d, tiene que ser mayor que cero", name, team)
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("grupo %q repetido", name)
		}
		out[name] = team
	}
	return out, nil
}

// parseKeyValueList lee "a=b,c=d" en un mapa; entradas vacías o sin '=' se ignoran.
func parseKeyValueList(raw string) map[string]string {
	out := map[string]string{}
	for _, item := range strings.Split(raw, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(item), "=")
		if ok && strings.TrimSpace(k) != "" {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

// ResolveOutboundWebhookSecret returns the outbound webhook secret, falling back to the main secret.
func (c *Config) ResolveOutboundWebhookSecret() string {
	if c.BirdWebhookSecretOutbound != "" {
		return c.BirdWebhookSecretOutbound
	}
	return c.BirdWebhookSecret
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if v == "true" || v == "1" {
		return true
	}
	if v == "false" || v == "0" {
		return false
	}
	return fallback
}

// DefaultCloseMarginMin y MaxCloseMarginMin acotan BUSINESS_HOURS_CLOSE_MARGIN_MIN, los minutos
// ANTES del cierre a partir de los cuales lo que llegue ya no lo alcanza a atender una persona
// hoy. El tope existe porque un margen mayor que la jornada la anularía entera —todo caería en
// la cola del próximo día—. 0 es legal: apaga la ventana sin tocar código.
const (
	DefaultCloseMarginMin = 15
	MaxCloseMarginMin     = 120
)

// DefaultQueueMaxWaitMin es lo máximo que puede faltar para la próxima apertura para que todavía
// tenga sentido ENCOLAR un caso (BUSINESS_HOURS_QUEUE_MAX_WAIT_MIN, en minutos; 20 h).
//
// El límite lo impone WhatsApp: la ventana de 24 h de servicio se cierra sola mientras el caso
// espera, y un caso del sábado que un asesor toma el lunes ya no se puede contestar sin
// plantilla. 20 h cubre el salto de una noche (viernes 18:00 → sábado 07:00, 13 h) y deja fuera
// el de un fin de semana (sábado 12:00 → lunes 07:00, 43 h). 0 apaga la cola entera.
const DefaultQueueMaxWaitMin = 1200

// validateCloseMarginMin rechaza un margen imposible.
func validateCloseMarginMin(minutes int) error {
	if minutes < 0 || minutes > MaxCloseMarginMin {
		return fmt.Errorf("margen de cierre %d fuera de rango (0..%d)", minutes, MaxCloseMarginMin)
	}
	return nil
}

// Valores de AGENTS_SOURCE.
const (
	AgentsSourceBird   = "bird"
	AgentsSourceNectar = "nectar"
)

// AgentsInNectar dice si el informe de agentes debe leerse en Néctar. main lo combina con el
// puente: sin puente armado los agentes siguen en Bird diga lo que diga AGENTS_SOURCE.
func (c *Config) AgentsInNectar() bool { return c.AgentsSource == AgentsSourceNectar }

// parseAgentsSource lee AGENTS_SOURCE: vacía = Bird, sin distinguir mayúsculas, y cualquier
// otro valor tumba el arranque — un typo no puede dejar el informe en la fuente equivocada en
// silencio.
func parseAgentsSource(raw string) (string, error) {
	switch v := strings.ToLower(strings.TrimSpace(raw)); v {
	case "":
		return AgentsSourceBird, nil
	case AgentsSourceBird, AgentsSourceNectar:
		return v, nil
	default:
		return "", fmt.Errorf("fuente %q desconocida (valores: bird, nectar)", raw)
	}
}

// parseCloseMarginMin lee BUSINESS_HOURS_CLOSE_MARGIN_MIN: vacío = el default, y cualquier otra
// cosa tiene que ser un número dentro del rango. NO se lee con getEnvInt porque ese se traga el
// error de Atoi: `=quince` arrancaría con el default en silencio.
func parseCloseMarginMin(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultCloseMarginMin, nil
	}
	minutes, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("margen de cierre %q no numérico", raw)
	}
	return minutes, validateCloseMarginMin(minutes)
}

// validateQueueMaxWaitMin rechaza el único valor imposible. No hay tope superior: un valor
// enorme deja la cola siempre encendida, que es una postura legítima del operador.
func validateQueueMaxWaitMin(minutes int) error {
	if minutes < 0 {
		return fmt.Errorf("espera máxima %d negativa", minutes)
	}
	return nil
}

// parseQueueMaxWaitMin lee BUSINESS_HOURS_QUEUE_MAX_WAIT_MIN: vacío = el default, y cualquier
// otra cosa tiene que ser un número no negativo. Mismo criterio que parseCloseMarginMin.
func parseQueueMaxWaitMin(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultQueueMaxWaitMin, nil
	}
	minutes, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("espera máxima %q no numérica", raw)
	}
	return minutes, validateQueueMaxWaitMin(minutes)
}

// DefaultNectarReportsSyncMin es cada cuántos minutos se sincronizan los informes de Néctar sin
// NECTAR_REPORTS_SYNC_MIN en el entorno.
const DefaultNectarReportsSyncMin = 5

// DefaultSiesaPendingRetryMin es cada cuántos minutos se reintentan las respuestas pendientes de
// SIESA sin SIESA_PENDING_RETRY_MIN en el entorno.
const DefaultSiesaPendingRetryMin = 5

// DefaultNectarCloseMin es el cierre por silencio de una escalación de Néctar (8 h). Es mucho
// más largo que el de Bird porque Néctar NO manda mensaje_entrante mientras el agente es dueño
// de la conversación: last_patient_msg_at no se mueve aunque paciente y agente estén hablando, y
// con 120 min se abandonaba la sesión de una atención larga y se perdía la conversacion_devuelta
// posterior. El fin normal de una escalación de Néctar es conversacion_resuelta; esto es solo la
// red de seguridad para que la sesión no quede escalada para siempre.
const DefaultNectarCloseMin = 480

// parseNectarCloseMin lee ESCALATION_NECTAR_CLOSE_MIN: vacío = el default, y cualquier otra cosa
// tiene que ser un número positivo. El 0 se rechaza: apagaría la red de seguridad y una
// escalación cuyo conversacion_resuelta se pierda quedaría escalada para siempre.
func parseNectarCloseMin(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultNectarCloseMin, nil
	}
	minutes, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("cierre de Néctar %q no numérico", raw)
	}
	if minutes <= 0 {
		return 0, fmt.Errorf("cierre de Néctar %d debe ser positivo", minutes)
	}
	return minutes, nil
}

// NectarLegacyConfigured dice si la línea vieja está configurada: ok solo con clave Y canal. Uno sin
// el otro es un error que nombra la variable que falta (arrancar a medias dejaría la línea muda);
// los dos vacíos es simplemente apagada (false, nil).
func (c *Config) NectarLegacyConfigured() (ok bool, err error) {
	hasKey := c.NectarLegacyServiceKey != ""
	hasChannel := c.NectarLegacyChannelID != 0
	switch {
	case hasKey && hasChannel:
		return true, nil
	case hasKey:
		return false, fmt.Errorf("falta NECTAR_LEGACY_CHANNEL_ID")
	case hasChannel:
		return false, fmt.Errorf("falta NECTAR_LEGACY_SERVICE_KEY")
	}
	return false, nil
}

// parseNectarChannelID lee NECTAR_CHANNEL_ID: vacío = 0 (apertura sin configurar) y cualquier otra
// cosa tiene que ser un id no negativo. Un valor ilegible tumba el arranque en vez de dejar mudas,
// en silencio, todas las plantillas proactivas.
func parseNectarChannelID(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("canal %q no numérico", raw)
	}
	if id < 0 {
		return 0, fmt.Errorf("canal %d negativo", id)
	}
	return id, nil
}

// parsePositiveInt lee un entero positivo de una variable de entorno: vacío = def, y cualquier otra
// cosa tiene que ser un número > 0. Mismo criterio que parseNectarCloseMin: no getEnvInt, que se
// traga el error de Atoi y arrancaría con el default en silencio.
func parsePositiveInt(raw string, def int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%q no numérico", raw)
	}
	if n <= 0 {
		return 0, fmt.Errorf("%d debe ser positivo", n)
	}
	return n, nil
}
