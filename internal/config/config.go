package config

import (
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

	// OpenAI
	OpenAIAPIKey string
	OpenAIModel  string

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
	EscalationPatientCloseMin  int // Cierre escalada por silencio del PACIENTE (no del agente)
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

	// Security
	InternalAPIKey string
	LogMaskPhones  bool // Mask phone numbers in logs (default true; set LOG_MASK_PHONES=false to disable)

	// Ngrok
	NgrokHostname string

	// Testing
	TestingAlwaysOpen bool // Bypasses business hours check when true
	MaxRetries        int  // Max invalid response attempts before fallback menu

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

	// Testing whitelist — only these phones can interact with the bot (empty = all allowed)
	TestingWhitelistPhones []string

	// Scaling — configurable pool sizes and DB connections for load profiles
	ScalingProfile    string // "normal" or "high-load" (informational, for monitoring alerts)
	WorkerPoolSize    int    // Worker goroutines (default 10)
	WorkerQueueSize   int    // Message queue buffer (default 100)
	LocalDBMaxOpen    int    // Local DB max open connections (default 25)
	LocalDBMaxIdle    int    // Local DB max idle connections (default 10)
	ExternalDBMaxOpen int    // External DB max open connections (default 10)
	ExternalDBMaxIdle int    // External DB max idle connections (default 5)
	HTTPReadTimeout   int    // HTTP server read timeout seconds (default 30)
	HTTPWriteTimeout  int    // HTTP server write timeout seconds (default 30)
	HTTPIdleTimeout   int    // HTTP server idle timeout seconds (default 60)
	MySQLMaxConns     int    // MySQL max_connections hint for docker-compose (informational)
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
		DBHost:     getEnv("DB_HOST", "db"),
		DBPort:     getEnv("DB_PORT", "3306"),
		DBDatabase: getEnv("DB_DATABASE", "neuro_bot"),
		DBUser:     getEnv("DB_USER", "botuser"),
		DBPassword: os.Getenv("DB_PASSWORD"),

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

		// OpenAI
		OpenAIAPIKey: os.Getenv("OPENAI_API_KEY"),
		OpenAIModel:  getEnv("OPENAI_MODEL", "gpt-4o-mini"),

		// Recuperación asistida por IA (3 env; modelo y max_tokens fijos en código)
		AIRecoveryEnabled:            getEnvBool("AI_RECOVERY_ENABLED", true),
		AIRecoveryMaxPatientAttempts: getEnvInt("AI_RECOVERY_MAX_PATIENT_ATTEMPTS", 2),
		AIRecoveryMonthlyLimit:       getEnvInt("AI_RECOVERY_MONTHLY_LIMIT", 0),

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

		// Security
		InternalAPIKey: os.Getenv("INTERNAL_API_KEY"),
		// #31 (auditoría): getEnvBool (acepta true/1/false/0, case-insensitive) en vez de == "true"
		// estricto — un "True"/"1" desactivaba el masking de teléfonos (PII) silenciosamente.
		LogMaskPhones: getEnvBool("LOG_MASK_PHONES", true),

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
		TestingWhitelistPhones: parsePhoneList(os.Getenv("TESTING_WHITELIST_PHONES")),

		// Scaling
		ScalingProfile:    getEnv("SCALING_PROFILE", "normal"),
		WorkerPoolSize:    getEnvInt("WORKER_POOL_SIZE", 10),
		WorkerQueueSize:   getEnvInt("WORKER_QUEUE_SIZE", 100),
		LocalDBMaxOpen:    getEnvInt("LOCAL_DB_MAX_OPEN", 25),
		LocalDBMaxIdle:    getEnvInt("LOCAL_DB_MAX_IDLE", 10),
		ExternalDBMaxOpen: getEnvInt("EXTERNAL_DB_MAX_OPEN", 10),
		ExternalDBMaxIdle: getEnvInt("EXTERNAL_DB_MAX_IDLE", 5),
		HTTPReadTimeout:   getEnvInt("HTTP_READ_TIMEOUT", 30),
		HTTPWriteTimeout:  getEnvInt("HTTP_WRITE_TIMEOUT", 30),
		HTTPIdleTimeout:   getEnvInt("HTTP_IDLE_TIMEOUT", 60),
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

func parsePhoneList(s string) []string {
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

// ResolveTeamForCups returns the Bird team ID based on the CUPS procedure code.
// Falls back to BirdTeamFallback (Call Center) for unknown codes.
// When TeamRoutingEnabled is false, always returns Call Center.
func (c *Config) ResolveTeamForCups(cupsCode string) string {
	if !c.TeamRoutingEnabled {
		return c.BirdTeamFallback
	}
	if len(cupsCode) < 3 {
		return c.BirdTeamFallback
	}
	p3 := cupsCode[:3]
	switch {
	case p3 == "881" || p3 == "882": // Ecografía
		return c.BirdTeamGrupoA
	case p3 == "883": // Resonancia Magnética
		return c.BirdTeamGrupoA
	case p3 == "871" || p3 == "879": // Tomografía (TAC)
		return c.BirdTeamGrupoA
	case p3 == "870" || (p3 >= "873" && p3 <= "878"): // Rayos X
		return c.BirdTeamGrupoA
	case p3 == "291" || p3 == "930" || p3 == "892": // EMG / Fisiatría
		return c.BirdTeamGrupoB
	case cupsCode == "890274" || cupsCode == "890374" || cupsCode == "053105": // Neurología
		return c.BirdTeamGrupoB
	default:
		return c.BirdTeamFallback
	}
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
