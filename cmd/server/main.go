package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/api"
	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"
	"github.com/neuro-bot/neuro-bot/internal/database"
	"github.com/neuro-bot/neuro-bot/internal/logging"
	"github.com/neuro-bot/neuro-bot/internal/monitor"
	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/notifications"
	"github.com/neuro-bot/neuro-bot/internal/observability"
	"github.com/neuro-bot/neuro-bot/internal/recovery"
	"github.com/neuro-bot/neuro-bot/internal/repository"
	localrepo "github.com/neuro-bot/neuro-bot/internal/repository/local"
	"github.com/neuro-bot/neuro-bot/internal/repository/siesa"
	"github.com/neuro-bot/neuro-bot/internal/scheduler"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/statemachine/handlers"
	tg "github.com/neuro-bot/neuro-bot/internal/telegram"
	"github.com/neuro-bot/neuro-bot/internal/tracking"
	"github.com/neuro-bot/neuro-bot/internal/utils"
	"github.com/neuro-bot/neuro-bot/internal/worker"
)

var startTime = time.Now()

const (
	// maxReplayAttempts: cuántas veces se replaya un mensaje del WAL al arrancar antes de darlo por
	// veneno y descartarlo. Bajo a propósito: si tres arranques seguidos no lograron procesarlo, el
	// problema no es transitorio y seguir intentando solo mantiene al bot en bucle de reinicio.
	maxReplayAttempts = 3
	// replayBatchLimit acota cuántas filas se cargan de una: cada una trae su raw_body completo.
	replayBatchLimit = 500
)

// Sello de build, inyectado con -ldflags -X al compilar (ver docker/Dockerfile). Sin él no había forma
// de saber QUÉ código corre en producción: /health/debug solo decía desde cuándo está arriba, no qué
// versión es. Eso llevó dos veces a discutir si un deploy había entrado o no (auditoría, ciclos 125 y
// 129) apoyándose en inferencias frágiles. Con esto, "¿está desplegado el fix?" se responde leyendo
// build_commit. Los valores por defecto aplican al compilar fuera de Docker (go run / go build local).
var (
	buildCommit = "unknown"
	buildTime   = "unknown"
)

func main() {
	// Capture signals explicitly so we can log which one we received
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(context.Background())

	cfg := config.Load()

	// Timezone ANTES de lanzar cualquier goroutine: time.Local es una variable global sin
	// sincronización y reasignarla con goroutines ya corriendo es una carrera de datos (D-12). La
	// señal no se pierde mientras tanto: signal.Notify ya la encola en sigCh (buffer 1).
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		log.Fatalf("invalid timezone %s: %v", cfg.Timezone, err)
	}
	time.Local = loc

	safeGo("signal-handler", func() {
		sig := <-sigCh
		slog.Info("signal received", "signal", sig.String())
		cancel()
	})

	utils.SetMaskPhones(cfg.LogMaskPhones)

	// Configurar logger
	initLogger(cfg.LogLevel, cfg.LogDir, cfg.LogMaxFileMB)

	slog.Info(
		"bot starting",
		"pid", os.Getpid(),
		"version", "1.0",
	)
	for _, w := range cfg.SecurityWarnings() {
		slog.Warn("insecure configuration", "detail", w)
	}

	// Capture panics in the log file (otherwise only visible in docker logs)
	defer func() {
		if r := recover(); r != nil {
			slog.Error(
				"PANIC — bot crashed",
				"error", fmt.Sprintf("%v", r),
				"stack", string(debug.Stack()),
			)
			panic(r) // re-panic so Docker sees non-zero exit
		}
	}()

	// Telegram error alerts (optional — wraps slog handler)
	tgClient := tg.NewClient(cfg.TelegramBotToken, cfg.TelegramChatID)
	if tgClient != nil {
		alertHandler := tg.NewAlertHandler(slog.Default().Handler(), tgClient)
		safeGo("telegram-alerts", func() { alertHandler.Start(ctx) })
		slog.SetDefault(slog.New(alertHandler))
		slog.Info("telegram error alerts enabled")
	}

	// Conectar BD local
	localDB, err := database.NewLocalDB(cfg)
	if err != nil {
		log.Fatalf("local db: %v", err)
	}
	defer localDB.Close()

	// Conectar BD externa (no fatal si falla — health check mostrará "degraded")
	externalDB, err := database.NewExternalDB(cfg)
	if err != nil {
		slog.Warn("external db not available, bot will start in degraded mode", "error", err)
	} else {
		defer externalDB.Close()
	}

	// Migraciones (BD local)
	if err := database.RunMigrations(localDB, "migrations"); err != nil {
		log.Fatalf("migrations: %v", err)
	}

	// Repositorios — selección por EXTERNAL_DB_DRIVER (R-ARQ-01)
	var repos *repository.Repositories
	if externalDB != nil {
		repos = initRepositories(cfg, externalDB, localDB)
	}

	// Session manager (BD local + phone mutex)
	sessionRepo := localrepo.NewSessionRepo(localDB)
	sessionManager := session.NewSessionManager(sessionRepo, cfg.SessionTimeoutMinutes)
	escalationRepo := localrepo.NewEscalationRepo(localDB)
	sessionManager.SetEscalationRecorder(escalationRepo)

	// Iniciar phone mutex cleanup
	safeGo("phone-mutex-cleanup", func() { sessionManager.PhoneMutex().StartCleanup(ctx) })

	// Bird client
	birdClient := bird.NewClient(cfg)
	safeGo("bird-cache-cleanup", func() { birdClient.StartCacheCleanup(ctx) })

	// Néctar: con NECTAR_BASE_URL puesta, las conversaciones nectar:* se desvían a las rutas
	// /api/bot/*; lo demás sigue en Bird. Configurado a medias → se registra y el bot sigue como Bird.
	nectarClient, nectarLegacyClient, nectarBridge, nectarLines, nectarErr := nectarBridgeFromConfig(cfg, localrepo.NewConversationLineRepo(localDB))
	if nectarErr != nil {
		if nectarBridge != nil {
			slog.Error("Néctar: la línea vieja queda APAGADA (la principal sigue activa)", "error", nectarErr)
		} else {
			slog.Error("el desvío a Néctar queda APAGADO", "error", nectarErr)
		}
	}
	// Los handlers (el anuncio de la línea nueva) preguntan por el puente a la configuración.
	cfg.NectarBridgeArmed = nectarBridge != nil
	// /health y el texto del aviso urgente leen esto: va ANTES de registrar las rutas y de cablear el aviso.
	cfg.NectarLegacyArmed = nectarBridge != nil && nectarLegacyClient != nil
	if nectarBridge != nil {
		birdClient.SetNectar(nectarBridge)
		slog.Info("Néctar activo", "host", nectar.HostOf(nectarClient.BaseURL()), "notice_secret", cfg.NectarNoticeSecret != "",
			"legacy_line", cfg.NectarLegacyArmed)
	}

	// El horario de atención y los festivos: todo el bot los pregunta por cfg.Hours(). Rige el de
	// siempre hasta que Néctar publique una lectura buena; con NECTAR_SCHEDULE_ENABLED apagada (el
	// default) de Néctar solo se toman los festivos. Solo con el puente activo hay de dónde leer.
	schedule := wireSchedule(cfg)
	var scheduleClient *nectar.Client
	if nectarBridge != nil {
		scheduleClient = nectarClient
	}

	// Aviso urgente de volver a Bird (Telegram directo, aparte de los errores comunes): solo con
	// Telegram y el puente encendido; scheduleClient es el cliente del puente (nil si está apagado).
	// wireNectarRuntime lo cablea ANTES de arrancar el horario (su primera petición a Néctar).
	urgent := wireNectarRuntime(ctx, cfg, tgClient, scheduleClient, schedule)
	if urgent != nil {
		safeGo("urgent-alerts", func() { urgent.Start(ctx) })
		slog.Info("aviso urgente de volver a Bird activo", "cooldown_min", cfg.UrgentAlertCooldownMin)
	}
	// Informes de asesores (D5): sincroniza tramos y roster de Néctar. Solo con el puente
	// encendido, y después de wireNectarRuntime (el aviso urgente ya cableado en el cliente).
	var legacyReportsClient *nectar.Client
	if cfg.NectarLegacyArmed {
		legacyReportsClient = nectarLegacyClient
	}
	startNectarReports(ctx, cfg, scheduleClient, legacyReportsClient, localrepo.NewNectarReportsRepo(localDB), escalationRepo)

	// Services (dependen de repos)
	var patientSvc *services.PatientService
	var appointmentSvc *services.AppointmentService

	// El prompt del OCR no lleva el catálogo: sin código visible el código lo asigna VALIDATE_OCR por
	// nombre (handlers/cups_name_assign.go).
	ocrSvc := services.NewOCRService(cfg.OpenAIAPIKey, cfg.OpenAIModel, cfg.BirdAccessKeyID)
	ocrSvc.SetDocumentModel(cfg.OpenAIDocModel)

	if nectarBridge != nil {
		ocrSvc.SetNectarCredentials(cfg.NectarBaseURL, cfg.NectarServiceKey, cfg.NectarRequestSecret)
		// Línea vieja: su clave de canal para los adjuntos que la principal no ve (404).
		if cfg.NectarLegacyArmed {
			ocrSvc.SetNectarFallbackCredentials(cfg.NectarLegacyServiceKey, cfg.NectarLegacyRequestSecret)
		}
	}

	if repos != nil {
		patientSvc = services.NewPatientService(repos.Patient)
		appointmentSvc = services.NewAppointmentService(repos.Appointment, cfg)
		appointmentSvc.SetConfirmationLog(localrepo.NewConfirmationLogRepo(localDB))
	}

	// State machine (interceptores + handlers se registran por fase)
	machine := statemachine.NewMachine()
	statemachine.SetMaxRetries(cfg.MaxRetries)
	statemachine.RegisterInterceptors(machine)
	// El acuse de la orden fuera de horario nombra el horario VIGENTE (el mismo de los gates).
	machine.SetBusinessHoursText(func() string { return cfg.Hours().DescribeEmphasized() })

	// Location repo (local DB)
	locationRepo := localrepo.NewLocationRepo(localDB)

	// Address mapper (maps procedure addresses → Google Maps URLs from center_locations)
	var addrMapper *services.AddressMapper
	if locations, err := locationRepo.FindActive(ctx); err == nil && len(locations) > 0 {
		addrMapper = services.NewAddressMapper(locations)
		slog.Info("address mapper loaded", "locations", len(locations))
	}

	// Interceptor de intención por foto en el menú (paquete handlers: necesita construir la lista de
	// entidades). Se añade DESPUÉS de RegisterInterceptors para que corra tras ImageOutOfContext, que ya
	// deja pasar MAIN_MENU. Convierte "foto en el menú" en el arranque del flujo de agendar (§3.6).
	machine.AddInterceptor(handlers.PhotoIntentInterceptor())
	// Orden como PRIMER mensaje → stash + arranque normal (§8.1 #1). Página ADICIONAL durante
	// "¿Es correcto?" → fusión de CUPS (§8.1 #3). Ambos tras ImageOutOfContext (que ya los deja pasar).
	machine.AddInterceptor(handlers.PhotoStartInterceptor(cfg))
	if ocrSvc != nil {
		machine.AddInterceptor(handlers.PhotoAppendInterceptor(ocrSvc))
	}

	// Aplicación de medicamentos: repositorio de listado pendiente (tabla medication_pending).
	medRepo := localrepo.NewMedicationPendingRepo(localDB)
	handlers.RegisterMedicationHandlers(machine, medRepo, ocrSvc)

	// Fase 5: Saludo e Identificación + Results/Locations
	handlers.RegisterGreetingHandlers(machine, cfg, locationRepo)
	handlers.RegisterResultsAndLocationHandlers(machine, cfg, locationRepo)
	if patientSvc != nil {
		handlers.RegisterIdentificationHandlers(machine, patientSvc)
	}
	// Fase 5.5: Entity Management (existing patients)
	if repos != nil {
		handlers.RegisterEntityManagementHandlers(machine, repos.Entity, repos.Patient)
	}
	// Fase 6: Registro de Pacientes
	if repos != nil && patientSvc != nil {
		handlers.RegisterRegistrationHandlers(machine, patientSvc, repos.Municipality)
	}
	// Fase 7: Consulta y Gestión de Citas
	// Cambio 13: CancellationCallback — notifyManager captured by reference (assigned later, before server accepts requests)
	var notifyManager *notifications.NotificationManager
	onCancel := handlers.CancellationCallback(func(ctx context.Context, codMedi, agendaID int) {
		if notifyManager != nil {
			notifyManager.CheckWaitingListForSlot(ctx, codMedi, agendaID)
		}
	})
	if appointmentSvc != nil {
		var procRepoForAppts repository.ProcedureRepository
		if repos != nil {
			procRepoForAppts = repos.Procedure
		}
		handlers.RegisterAppointmentHandlers(machine, appointmentSvc, procRepoForAppts, addrMapper, onCancel, cfg)
	}
	// Fase 8: Orden Médica y OCR
	waitingListRepo := localrepo.NewWaitingListRepo(localDB)
	if repos != nil {
		handlers.RegisterMedicalOrderHandlers(machine, ocrSvc, repos.Procedure, birdClient, waitingListRepo, appointmentSvc, medRepo)
	}
	// Fase 9: Validaciones Médicas
	gfrSvc := services.NewGFRService()
	if appointmentSvc != nil {
		var procRepoForGFR repository.ProcedureRepository
		if repos != nil {
			procRepoForGFR = repos.Procedure
		}
		handlers.RegisterMedicalValidationHandlers(machine, gfrSvc, appointmentSvc, procRepoForGFR, cfg)
	}
	// Fase 10 + 13: Búsqueda de Slots y Agendamiento + Lista de Espera
	var slotSvc *services.SlotService
	if repos != nil && appointmentSvc != nil {
		slotSvc = services.NewSlotService(repos.Procedure, repos.Schedule)
		handlers.RegisterSlotHandlers(machine, slotSvc, appointmentSvc, repos.Procedure, repos.Price, repos.Entity, waitingListRepo, addrMapper, birdClient)
	}
	// Fase 11: Post-Acción y Escalación
	handlers.RegisterPostActionHandlers(machine, birdClient)
	handlers.RegisterEscalationHandlers(machine, birdClient, cfg, escalationRepo)

	// Recuperación asistida por IA (capa antes de escalar; docs/RECUPERACION-IA.md). Se cablea tras
	// registrar los handlers para que la Machine tenga los metadatos de opt-in por estado. Requiere
	// API key de OpenAI; usa su propio modelo (distinto al del OCR). Tope mensual: Fase 3 (nil = sin).
	if cfg.AIRecoveryEnabled && cfg.OpenAIAPIKey != "" {
		llm := recovery.NewLLMClient(cfg.OpenAIAPIKey, recovery.DefaultModel, recovery.DefaultMaxOutputTokens)
		coord := recovery.NewCoordinator(machine, recovery.NewAIRecovery(llm), recovery.Config{
			Enabled:            true,
			MaxPatientAttempts: cfg.AIRecoveryMaxPatientAttempts,
			Monthly:            localrepo.NewAIRecoveryCounterRepo(localDB, cfg.AIRecoveryMonthlyLimit),
		})
		machine.SetRecoveryCoordinator(coord)
		slog.Info("ai_recovery_enabled", "model", recovery.DefaultModel, "max_patient_attempts", cfg.AIRecoveryMaxPatientAttempts)
	}

	// Fase 12: Notificaciones Proactivas y Scheduler
	if appointmentSvc != nil {
		notifyManager = notifications.NewNotificationManager(birdClient, appointmentSvc, cfg)
		if urgent != nil {
			notifyManager.SetUrgentReporter(urgent)
		}
	}

	// Fase 14: Event Tracking
	eventRepo := localrepo.NewEventRepo(localDB)
	eventRepo.SetProactiveLineTagging(nectarBridge != nil)
	tracker := tracking.NewEventTracker(eventRepo)

	// Observabilidad de flujos (Fase 0): traza de negocio estructurada (flow_events).
	flowRepo := localrepo.NewFlowRepo(localDB)
	flowTracer := observability.New(flowRepo, observability.ParseLevel(cfg.FlowTraceLevel))
	observability.Init(flowTracer)
	safeGo("flow-tracer", func() { flowTracer.Start(ctx) })

	// Fase 22: Notification persistence + preparations + tracking
	notifRepo := localrepo.NewNotificationRepo(localDB)
	callRepo := localrepo.NewCallRepo(localDB)
	if notifyManager != nil {
		notifyManager.SetPersister(notifRepo)
		notifyManager.SetSiesaPendingStore(notifRepo)
		notifyManager.SetCallTracker(callRepo)
		notifyManager.SetTracker(tracker)
		notifyManager.SetEscalationRecorder(escalationRepo)
		if repos != nil {
			notifyManager.SetProcedureRepo(repos.Procedure)
		}
		if addrMapper != nil {
			notifyManager.SetAddressMapper(addrMapper)
		}
		notifyManager.RestorePending(ctx)
		safeGo("notification-expiry", func() { notifyManager.StartExpirationChecker(ctx) })
		safeGo("siesa-pending-retry", func() { notifyManager.StartSiesaPendingRetrier(ctx) })
	}

	// Message inbox (WAL for crash recovery)
	inboxRepo := localrepo.NewInboxRepo(localDB)

	// Worker pool (configurable via WORKER_POOL_SIZE / WORKER_QUEUE_SIZE)
	workerPool := worker.NewMessageWorkerPool(cfg.WorkerPoolSize, cfg.WorkerQueueSize)
	workerPool.SetDependencies(sessionManager, birdClient, machine)
	machine.SetPreEscalationFlush(workerPool.PreEscalationFlush)
	workerPool.SetTracker(tracker)
	workerPool.SetAgentActivityChecker(escalationRepo) // acuse durante escalación (§8.1 #6)
	workerPool.SetOCRService(ocrSvc)
	workerPool.SetInboxRepo(inboxRepo)
	workerPool.SetBotEnabled(cfg.BotEnabled)
	// El horario EN PALABRAS: lo nombra el aviso de cola vencida (Néctar devuelve el caso que
	// nadie tomó y el bot le dice al paciente cuándo volver). Sale del mismo horario que el gate.
	workerPool.SetBusinessHoursText(func() string { return handlers.BusinessHoursDescription(cfg) })
	// Con la cola del próximo día, el pre-envío quita el aviso genérico de conexión (y solo él): el
	// paciente lee lo demás del turno y el aviso del próximo día hábil, que manda el handler.
	workerPool.SetEscalationQueuedNextDay(handlers.NextDayQueueApplies(cfg))
	if notifyManager != nil {
		workerPool.SetNotifyResponder(notifyManager)
	}
	workerPool.Start(ctx)

	// Fase 20: Inactivity checker (single reminder + silent close for active, expire for escalated).
	// Va DESPUÉS del worker pool porque le inyecta el Resumer (workerPool) para el no-show de escalación
	// (devolver al bot las escalaciones que el agente nunca atendió — necesita la state machine del worker).
	safeGo("inactivity-checker", func() {
		sessionManager.StartInactivityChecker(ctx, session.InactivityDeps{
			BirdClient:               birdClient,
			Tracker:                  tracker,
			ReminderMin:              cfg.InactivityReminderMin,
			CloseMin:                 cfg.InactivityCloseMin,
			EscalationCloseMin:       cfg.EscalationPatientCloseMin,
			NectarEscalationCloseMin: cfg.EscalationNectarCloseMin,
			AgentReminderMin:         cfg.EscalationAgentReminderMin,
			AgentReminderMax:         cfg.EscalationAgentReminderMax,
			Resumer:                  workerPool,
			// Hasta cuándo espera una escalación encolada para el próximo día hábil: acota su
			// exención del cierre por silencio y del no-show (sin esto no caducaría nunca).
			NextOpen: func(t time.Time) time.Time { return handlers.QueuedWaitEnd(cfg, t) },
		})
	})

	// Capacity monitor — sends Telegram alerts when approaching limits
	capMon := monitor.New(monitor.Config{
		TGClient:          tgClient,
		WorkerPool:        workerPool,
		LocalDB:           localDB,
		ExternalDB:        externalDB,
		Profile:           cfg.ScalingProfile,
		LocalDBMaxOpen:    cfg.LocalDBMaxOpen,
		ExternalDBMaxOpen: cfg.ExternalDBMaxOpen,
		WorkerCount:       cfg.WorkerPoolSize,
	})
	if capMon != nil {
		safeGo("capacity-monitor", func() { capMon.Start(ctx) })
	}

	// Webhook handler (con NotificationManager para postbacks proactivos + WAL inbox). Se construye
	// ANTES del replay del WAL porque las filas de Néctar (msg_type nectar_notice) se despachan a
	// través de él.
	webhookHandler := api.NewWebhookHandler(birdClient, workerPool, notifyManager, cfg)
	webhookHandler.SetInboxRepo(inboxRepo)
	// Sin puente (vuelta a Bird), los avisos de Néctar se acusan y se descartan.
	webhookHandler.SetNectarBridgeOn(nectarBridge != nil)
	// Sin línea vieja no hay registro que leer ni escribir: todo es la principal (nil), sin SELECT en el
	// camino caliente del contacto.
	if cfg.NectarLegacyArmed {
		webhookHandler.SetNectarLines(nectarLines)
	}

	// WAL replay: re-process messages that weren't completed before last shutdown/crash.
	//
	// Protección de mensaje veneno (migración 035): un mensaje cuyo procesamiento MATA al proceso
	// nunca se marca 'done', así que sin tope se replaya en cada arranque y el bot queda en bucle de
	// reinicio permanente (28-jul-2026: ~2h de caída a partir de UN mensaje). Se descarta lo que ya
	// agotó sus intentos ANTES de replayar, y el intento se persiste ANTES de encolar.
	if poisoned, err := inboxRepo.PoisonExhausted(ctx, maxReplayAttempts); err != nil {
		slog.Error("inbox poison check failed", "error", err)
	} else if len(poisoned) > 0 {
		// slog.Error → alerta a Telegram: un mensaje descartado es pérdida de datos, hay que mirarlo.
		slog.Error("inbox replay: mensajes descartados por agotar reintentos (posible crash al procesarlos)",
			"count", len(poisoned), "max_attempts", maxReplayAttempts, "ids", poisoned)
	}

	if pending, err := inboxRepo.FindPending(ctx, replayBatchLimit); err != nil {
		slog.Error("inbox replay query failed", "error", err)
	} else if len(pending) > 0 {
		slog.Info("replaying unprocessed inbox messages", "count", len(pending))
		ids := make([]string, len(pending))
		for i, row := range pending {
			ids[i] = row.ID
		}
		if err := inboxRepo.MarkReplayAttempt(ctx, ids); err != nil {
			slog.Error("inbox replay attempt counter failed", "error", err)
		}
		deps := replayDeps{nectar: webhookHandler, pool: workerPool, inbox: inboxRepo}
		if notifyManager != nil { // una interfaz con un *NotificationManager nil NO sería nil
			deps.notify = notifyManager
		}
		replayPendingRows(ctx, pending, deps)
	}

	// M7: re-replay periódico de mensajes 'pending' atascados (p.ej. descartados por backpressure),
	// para recuperarlos sin esperar al reinicio del proceso.
	safeGo("stale-replay", func() {
		workerPool.StartStaleReplay(ctx, inboxReplaySource{repo: inboxRepo, nectar: webhookHandler})
	})

	// Fase 13: Inyectar dependencias de lista de espera al NotificationManager
	if notifyManager != nil {
		notifyManager.SetWaitingListDeps(waitingListRepo, sessionRepo, workerPool)
	}

	// Cambio 13: Inyectar dependencias para WL check en tiempo real
	if notifyManager != nil && slotSvc != nil && repos != nil {
		notifyManager.SetWaitingListCheckDeps(slotSvc, repos.Appointment, waitingListRepo, repos.Schedule, repos.Procedure)
		// Teléfono en SIESA de las entradas de lista de espera nacidas en Néctar (guardan "nectar:<id>").
		notifyManager.SetPatientReader(repos.Patient)
	}

	var schedulerTasks *scheduler.Tasks
	var sched *scheduler.Scheduler
	if repos != nil && notifyManager != nil {
		schedulerRunRepo := localrepo.NewSchedulerRunRepo(localDB)

		sched = scheduler.NewScheduler(loc)
		sched.SetRunRepo(schedulerRunRepo)
		schedulerTasks = &scheduler.Tasks{
			AppointmentRepo: repos.Appointment,
			AppointmentSvc:  appointmentSvc,
			BirdClient:      birdClient,
			NotifyManager:   notifyManager,
			WaitingListRepo: waitingListRepo,
			SlotService:     slotSvc,
			ProcedureRepo:   repos.Procedure,
			Cfg:             cfg,
			Tracker:         tracker,
			InboxRepo:       inboxRepo,
			NotifHistory:    eventRepo,
			ReengageSource:  eventRepo, // re-enganche matutino (par.8.1 #9) // dedup del recordatorio de corta antelación (fail-closed si nil)
			ChatEventsPII:   eventRepo, // retención de PII de chat_events (C-04)
		}
		// Reconciliación de invariantes (Fase 2 observabilidad): checks locales + SIESA (bot-filtered).
		reconciler := observability.NewReconciler()
		reconciler.Register("wl_stuck", observability.StuckWaitingListCheck(localDB, 25))
		// graceHours=6: la vida legítima MÁXIMA de una escalada sin agente es último-mensaje-del-
		// paciente + 5h (no-show = AgentReminderMin 75 × (Max 3 + 1)), es decir expires_at + ~3h.
		// Con la gracia vieja (1h) el reconcile de las 02:00 marcaba "zombie" escalaciones nocturnas
		// EN CURSO que el no-show cerraba solo 45 min después (falso positivo del 13-ago-2026,
		// sess:11c0af50 — nació al extender el no-show la vida de la escalada sin actualizar esto).
		// 6h = 3h de brecha real + margen; un zombie que sobreviva 6h tras expirar sí es un atasco real.
		reconciler.Register("zombie_escalated", observability.ZombieEscalatedCheck(localDB, 6))
		if externalDB != nil {
			reconciler.Register("orphan_slot", observability.OrphanSlotCheck(externalDB, 4, cfg.SIESAAssignUserCedula))
			reconciler.Register("consulta_valor_cero", observability.ConsultaValorCeroCheck(externalDB, 4, cfg.SIESAAssignUserCedula))
		}
		schedulerTasks.Reconciler = reconciler
		schedulerTasks.FlowMaint = flowRepo // rollup + purga de flow_events (Fase 3)
		if urgent != nil {
			schedulerTasks.Urgent = urgent
		}

		schedulerTasks.RegisterAll(sched)
		sched.RunMissedTasks(ctx) // Catch-up missed tasks before starting the regular loop
		safeGo("scheduler", func() { sched.Start(ctx) })
	}

	// (webhookHandler se construye antes del replay del WAL: ver arriba)
	// Supresión de templates a números sin WhatsApp: el webhook outbound registra los fallos de
	// entrega y los recordatorios/lista de espera consultan el contador antes de enviar.
	deliveryRepo := localrepo.NewDeliveryRepo(localDB)
	webhookHandler.SetDeliveryTracker(deliveryRepo)
	if notifyManager != nil {
		notifyManager.SetDeliverability(deliveryRepo)
	}
	safeGo("gather-cleanup", func() { webhookHandler.StartGatherCleanup(ctx) })

	// Fase 13+14: Internal API endpoints (protegidos con API key)
	startTime := time.Now()
	var internalHandler *api.InternalHandler
	if repos != nil && notifyManager != nil {
		internalHandler = api.NewInternalHandler(
			repos.Appointment, repos.Schedule, waitingListRepo, eventRepo,
			birdClient, notifyManager, notifyManager, workerPool,
			tracker, cfg, startTime,
		)
		if schedulerTasks != nil {
			internalHandler.SetReminderRunner(schedulerTasks)
		}
		// Agentes en Néctar solo con puente real Y AGENTS_SOURCE=nectar: en la transición el puente
		// está encendido pero la mayoría de agentes sigue en Bird.
		internalHandler.SetAgentsInNectar(nectarBridge != nil && cfg.AgentsInNectar())
		if urgent != nil {
			internalHandler.SetUrgentReporter(urgent)
		}
		internalHandler.SetSessionReader(sessionRepo)
		internalHandler.SetFlowReader(flowRepo)
		if repos.Patient != nil {
			internalHandler.SetPatientReader(repos.Patient) // resolver teléfono/nombre al notificar (módulo Agenda)
		}
		// Catálogos de referencia de SIESA (médicos, asuntos) para el módulo de catálogo del
		// dashboard. Solo lectura, cacheados 30 min (casi estáticos) → no golpean SIESA por carga.
		if externalDB != nil {
			internalHandler.SetSiesaRefReader(siesa.NewReferenceRepo(externalDB, 30*time.Minute))
			// KPIs agregados de SIESA (ocupación, citas por estado, conciliación) para el dashboard.
			// Cache 10 min; la conciliación cruza con cups_medico (repos.Procedure, BD local).
			if repos != nil && repos.Procedure != nil {
				// P5 (auditoría queries): pool SIESA DEDICADO y chico para los KPIs, para que una
				// ráfaga del dashboard no deje sin conexiones al flujo de agendamiento. Si no se
				// puede abrir, cae al pool principal (comportamiento previo).
				kpiDB, kerr := database.NewSIESAKPIDB(cfg)
				if kerr != nil || kpiDB == nil {
					slog.Warn("kpi db pool not available, analytics will share the main SIESA pool", "error", kerr)
					kpiDB = externalDB
				} else {
					defer func() { _ = kpiDB.Close() }()
				}
				internalHandler.SetSiesaAnalyticsReader(siesa.NewAnalyticsRepo(kpiDB, 10*time.Minute), repos.Procedure)
			}
		}
	}

	// HTTP Server
	mux := http.NewServeMux()
	// nectar_host refleja el puente REAL: con la configuración a medias el puente quedó apagado y
	// la salud tiene que decir "none", no el host al que se quería apuntar.
	nectarHealthURL := ""
	if nectarBridge != nil {
		nectarHealthURL = cfg.NectarBaseURL
	}
	mux.HandleFunc("GET /health", healthHandler(localDB, externalDB, nectarHealthURL, cfg.NectarLegacyArmed))
	mux.HandleFunc("POST /api/webhooks/whatsapp", webhookHandler.HandleWhatsApp)
	mux.HandleFunc("POST /api/webhooks/whatsapp/outbound", webhookHandler.HandleWhatsAppOutbound)
	mux.HandleFunc("POST /api/webhooks/conversations", webhookHandler.HandleConversation)
	mux.HandleFunc("POST /api/webhooks/voice", webhookHandler.HandleVoiceWebhook)
	mux.HandleFunc("POST /api/webhooks/voice/dtmf", webhookHandler.HandleVoiceDTMF)
	// Néctar: avisos entrantes (cuatro kinds) y registro de capacidades. Se registran siempre: con
	// NECTAR_BASE_URL vacío nadie los invoca y el tráfico de Bird no cambia. El patrón de las
	// capacidades (con su método) lo comparten el mux y las pruebas.
	mux.HandleFunc("POST /api/webhooks/nectar", webhookHandler.HandleNectarNotice)
	// Avisos de la línea vieja: 404 mientras NECTAR_LEGACY_* no esté configurada.
	mux.HandleFunc("POST /api/webhooks/nectar/legacy", webhookHandler.HandleNectarLegacyNotice)
	mux.HandleFunc(api.NectarCapabilitiesPattern, webhookHandler.HandleNectarCapabilities)

	var internalRoutes http.Handler
	if internalHandler != nil {
		internalMux := http.NewServeMux()
		internalMux.HandleFunc("POST /api/internal/cancel-agenda", internalHandler.HandleCancelAgenda)
		internalMux.HandleFunc("POST /api/internal/reschedule-agenda", internalHandler.HandleRescheduleAgenda)
		internalMux.HandleFunc("POST /api/internal/appointment/{id}/cancel", internalHandler.HandleCancelAppointment)
		internalMux.HandleFunc("POST /api/internal/appointment/{id}/notify-confirmation", internalHandler.HandleNotifyConfirmation)
		internalMux.HandleFunc("POST /api/internal/waiting-list/check", internalHandler.HandleWaitingListCheck)
		internalMux.HandleFunc("GET /api/internal/waiting-list", internalHandler.HandleWaitingListGet)
		// (Eliminados los endpoints /api/internal/kpis/* — motor de KPIs duplicado/no consumido; el
		// dashboard calcula sus KPIs leyendo MySQL directo. Ver docs/ANALISIS-KPIS.md.)
		internalMux.HandleFunc("POST /api/internal/test-alert", internalHandler.HandleTestAlert)
		internalMux.HandleFunc("POST /api/internal/send-reminders", internalHandler.HandleSendReminders)
		internalMux.HandleFunc("POST /api/internal/send-agenda-confirmations", internalHandler.HandleSendAgendaConfirmations)
		internalMux.HandleFunc("POST /api/internal/test-voice-call", internalHandler.HandleTestVoiceCall)
		internalMux.HandleFunc("GET /api/internal/logs", internalHandler.HandleLogs)
		internalMux.HandleFunc("GET /api/internal/events", internalHandler.HandleEvents)
		internalMux.HandleFunc("GET /api/internal/sessions", internalHandler.HandleSessions)
		internalMux.HandleFunc("GET /api/internal/sessions/context", internalHandler.HandleSessionContext)
		internalMux.HandleFunc("GET /api/internal/flow-trace", internalHandler.HandleFlowTrace)
		internalMux.HandleFunc("GET /api/internal/flow-events", internalHandler.HandleFlowEvents)
		internalMux.HandleFunc("GET /api/internal/anomalies", internalHandler.HandleAnomalies)
		internalMux.HandleFunc("GET /api/internal/flow-stats", internalHandler.HandleFlowStats)
		// Catálogos de referencia de SIESA (read-only) para el módulo de catálogo del dashboard.
		internalMux.HandleFunc("GET /api/internal/siesa/medicos", internalHandler.HandleSiesaMedicos)
		internalMux.HandleFunc("GET /api/internal/siesa/asuntos", internalHandler.HandleSiesaAsuntos)
		internalMux.HandleFunc("GET /api/internal/siesa/agendas", internalHandler.HandleSiesaAgendas)
		internalMux.HandleFunc("GET /api/internal/siesa/doctor-agendas-on-date", internalHandler.HandleSiesaDoctorAgendasOnDate)
		internalMux.HandleFunc("GET /api/internal/siesa/agenda-appointments", internalHandler.HandleSiesaAgendaAppointments)
		// KPIs agregados de SIESA (solo lectura, cacheados) para las vistas del dashboard.
		internalMux.HandleFunc("GET /api/internal/siesa/ocupacion", internalHandler.HandleSiesaOcupacion)
		internalMux.HandleFunc("GET /api/internal/siesa/citas-estado", internalHandler.HandleSiesaCitasEstado)
		internalMux.HandleFunc("GET /api/internal/siesa/no-show", internalHandler.HandleSiesaNoShow)
		internalMux.HandleFunc("GET /api/internal/agents/bird", internalHandler.HandleBirdAgents)
		internalMux.HandleFunc("GET /api/internal/agents/bird-calls", internalHandler.HandleBirdAgentCalls)
		internalMux.HandleFunc("GET /api/internal/siesa/conversion", internalHandler.HandleSiesaConversion)
		internalMux.HandleFunc("GET /api/internal/siesa/mrc-consumption", internalHandler.HandleMRCConsumption)
		internalMux.HandleFunc("GET /api/internal/siesa/bot-share", internalHandler.HandleSiesaBotShare)
		internalMux.HandleFunc("GET /api/internal/siesa/conciliacion", internalHandler.HandleSiesaConciliacion)
		internalMux.HandleFunc("GET /api/internal/siesa/slot-recovery", internalHandler.HandleSiesaSlotRecovery)
		internalRoutes = internalMux
	}
	mountProtected(mux, internalGuard(cfg),
		http.HandlerFunc(debugHandler(localDB, externalDB, workerPool, cfg, nectarBridge != nil)), internalRoutes)

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      api.SecurityHeaders(api.RequestLogger(mux)),
		ReadTimeout:  time.Duration(cfg.HTTPReadTimeout) * time.Second,
		WriteTimeout: time.Duration(cfg.HTTPWriteTimeout) * time.Second,
		IdleTimeout:  time.Duration(cfg.HTTPIdleTimeout) * time.Second,
	}
	safeGo("http-server", func() {
		slog.Info(
			"server starting",
			"port", cfg.Port,
			"timezone", cfg.Timezone,
			"workers", cfg.WorkerPoolSize,
			"queue_size", cfg.WorkerQueueSize,
			"local_db_conns", cfg.LocalDBMaxOpen,
			"external_db_conns", cfg.ExternalDBMaxOpen,
		)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	})

	<-ctx.Done()
	slog.Info("shutting down...")

	// 1. Stop accepting new HTTP requests
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "error", err)
	}

	// 2. Drenar tareas del scheduler en vuelo (N-37) antes de cerrar conexiones (DB se cierra en los
	// defers al retornar). El ctx ya fue cancelado, así que las tareas paran rápido (N-38); las que
	// terminan persisten su last-run para no re-ejecutarse en el arranque. Corre a la par del paso 3:
	// no dependen entre sí, y así la espera de las respuestas no alarga el apagado.
	schedDone := make(chan struct{})
	go func() {
		defer close(schedDone)
		if sched != nil {
			sched.Wait(20 * time.Second)
		}
	}()

	// 3. Respuestas a plantillas en vuelo y, después, el pool de workers (stopWorkers, D-10).
	waitResponses := func(time.Duration) {}
	if notifyManager != nil {
		waitResponses = notifyManager.WaitResponses
	}
	if stopWorkers(waitResponses, workerPool.Stop, responsesWaitTimeout, workersStopTimeout) {
		slog.Info("shutdown complete")
	}
	<-schedDone

	// 4. #33: esperar a las auditorías SIESA fire-and-forget en vuelo ANTES de que los defer cierren
	// el pool externo (si no, una auditoría a medio camino falla con "sql: database is closed"). El
	// pool de workers ya paró (no se disparan nuevas), así que esto solo drena lo que quedó en vuelo.
	// Best-effort: si vence el timeout, el apagado continúa igual (la cita ya está committeada).
	if repos != nil {
		if bw, ok := repos.Appointment.(interface{ WaitBackground(time.Duration) }); ok {
			bw.WaitBackground(10 * time.Second)
		}
	}
}

func initLogger(level, logDir string, maxFileMB int) {
	var logLevel slog.Level
	switch level {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	var w io.Writer = os.Stdout
	if logDir != "" {
		fw, err := logging.NewDailyFileWriter(logDir, "neuro-bot", 30, int64(maxFileMB)*1024*1024)
		if err != nil {
			log.Printf("WARN: could not init log file writer: %v (logging to stdout only)", err)
		} else {
			w = io.MultiWriter(os.Stdout, fw)
		}
	}

	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: logLevel,
		// K3: redacción global de PII (Ley 1581) en TODOS los logs (stdout/archivo). Las claves
		// sensibles (documento/nombre/teléfono) se enmascaran antes de escribirse; el texto libre del
		// paciente (clave `text`) queda como «[redactado: N caracteres]», nunca en claro. El handler de
		// Telegram tiene además su propia redacción para su formato.
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Value.Kind() == slog.KindString {
				if red := utils.RedactLogAttr(a.Key, a.Value.String()); red != a.Value.String() {
					a.Value = slog.StringValue(red)
				}
			}
			return a
		},
	})
	slog.SetDefault(slog.New(handler))
}

// initRepositories construye los repositorios según el driver configurado.
// externalDB = SIESA (SQL Server). localDB = MySQL local del bot
// (catálogo cups_procedimientos, migración 019).
// "siesa" es el único driver soportado; el legacy datosipsndx (Antares/MySQL) fue eliminado.
func initRepositories(cfg *config.Config, externalDB, localDB *sql.DB) *repository.Repositories {
	switch cfg.ExternalDBDriver {
	case "siesa":
		// Datos clínicos → SIESA SQL Server (externalDB).
		// Catálogo CUPS (nombre, asunto_id, servicio_id, preparación) → BD local (migración 019).
		// Doctor names come resolved from appointment queries (RTRIM(sis_medi.nombre)),
		// so there is no separate doctor repository.
		return &repository.Repositories{
			Patient:      siesa.NewPatientRepo(externalDB),
			Appointment:  siesa.NewAppointmentRepo(externalDB, cfg.SIESAAssignUserCedula, cfg.SIESAAssignUserID),
			Schedule:     siesa.NewScheduleRepo(externalDB),
			Procedure:    repository.NewCachedProcedureRepo(localrepo.NewProcedureRepo(localDB), 60*time.Minute),
			Entity:       repository.NewCachedEntityRepo(siesa.NewEntityRepo(externalDB), 30*time.Minute),
			Price:        repository.NewCachedPriceRepo(siesa.NewPriceRepo(externalDB), 30*time.Minute),
			Municipality: siesa.NewMunicipalityRepo(externalDB),
		}
	default:
		log.Fatalf("unknown EXTERNAL_DB_DRIVER: %s", cfg.ExternalDBDriver)
		return nil
	}
}

// N-47: cache en memoria del resultado del health-check para evitar hacer Ping a
// localDB y externalDB (SIESA) en cada hit a /health. Los Pings solo se ejecutan si
// el ultimo resultado tiene mas de healthCacheTTL de antiguedad.
const healthCacheTTL = 5 * time.Second

type healthCache struct {
	mu        sync.Mutex
	lastCheck time.Time
	health    map[string]string
	critical  bool
}

// internalGuard protege lo que pide INTERNAL_API_KEY (la API interna y /health/debug) con un solo
// límite de intentos FALLIDOS: las dos rutas comparten el cupo de cada cliente, así que alternarlas
// no duplica los intentos de adivinar la clave; las peticiones con la clave buena no lo gastan.
func internalGuard(cfg *config.Config) func(http.Handler) http.Handler {
	limit := api.AuthFailureLimiter(30, time.Minute, api.NewTrustedProxies(cfg.TrustedProxyHosts))
	auth := api.InternalAuth(cfg.InternalAPIKey)
	return func(next http.Handler) http.Handler {
		return limit(api.MaxBodySize(auth(next)))
	}
}

// mountProtected registra las rutas que piden INTERNAL_API_KEY, todas tras el mismo guard: /health/debug
// y, si hay handler interno, /api/internal/.
func mountProtected(mux *http.ServeMux, guard func(http.Handler) http.Handler, debug, internal http.Handler) {
	mux.Handle("GET /health/debug", guard(debug))
	if internal != nil {
		mux.Handle("/api/internal/", guard(internal))
	}
}

// healthHandler responde el estado del bot. nectarBaseURL se publica como `nectar_host` (SOLO el host:
// ni claves ni ruta), o "none". legacyArmed (opcional) publica `nectar_legacy: armed` solo cuando la
// línea vieja de Néctar quedó armada; sin ella el cuerpo es el de siempre.
func healthHandler(localDB, externalDB *sql.DB, nectarBaseURL string, legacyArmed ...bool) http.HandlerFunc {
	cache := &healthCache{}
	legacy := len(legacyArmed) > 0 && legacyArmed[0]

	return func(w http.ResponseWriter, r *http.Request) {
		cache.mu.Lock()
		// N-47: si el resultado cacheado sigue vigente, responderlo sin Ping.
		if cache.health == nil || time.Since(cache.lastCheck) >= healthCacheTTL {
			health := map[string]string{"status": "ok"}
			critical := false

			// El resultado se comparte con todos durante healthCacheTTL: el Ping no hereda la
			// cancelación de quien pregunta (un cliente que corta no deja cacheado un «critical»),
			// solo su propio timeout. Cada BD tiene el suyo: una colgada no deja a la otra sin plazo.
			base := context.WithoutCancel(r.Context())
			ping := func(ctx context.Context, db *sql.DB) error {
				ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
				return db.PingContext(ctx)
			}

			if err := ping(base, localDB); err != nil {
				health["local_db"] = "error"
				health["status"] = "critical"
				critical = true
			} else {
				health["local_db"] = "ok"
			}

			if externalDB == nil {
				health["external_db"] = "not connected"
				if health["status"] == "ok" {
					health["status"] = "degraded"
				}
			} else if err := ping(base, externalDB); err != nil {
				health["external_db"] = "error"
				if health["status"] == "ok" {
					health["status"] = "degraded"
				}
			} else {
				health["external_db"] = "ok"
			}

			health["nectar_host"] = nectarHealth(nectarBaseURL)
			if legacy {
				health["nectar_legacy"] = "armed"
			}

			cache.health = health
			cache.critical = critical
			cache.lastCheck = time.Now()
		}

		health := cache.health
		critical := cache.critical
		cache.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if critical {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		json.NewEncoder(w).Encode(health)
	}
}

// inboxReplaySource adapta el inbox repo a worker.StalePendingSource: trae las filas 'pending'
// viejas y parsea su raw_body a InboundMessage (M7). Las filas no parseables se marcan 'done' para
// no reintentarlas en cada corrida (igual que el replay de arranque).
type inboxReplaySource struct {
	repo staleInboxRepo
	// nectar despacha las filas de Néctar (msg_type nectar_notice), que no son eventos de Bird.
	// SIEMPRE asignado (main lo construye con el webhookHandler y las pruebas también): no hay un
	// valor nulo válido, y dejarlo nulo falla ruidoso en la primera fila de Néctar en vez de
	// dejarla 'pending' para siempre.
	nectar nectarReplayer
}

// staleInboxRepo es lo que el replay de atascados necesita del inbox repo (*localrepo.InboxRepo).
type staleInboxRepo interface {
	FindPendingOlderThan(ctx context.Context, minutes int) ([]localrepo.InboxRow, error)
	inboxCloser
}

func (s inboxReplaySource) PendingOlderThan(ctx context.Context, minutes int) ([]bird.InboundMessage, error) {
	rows, err := s.repo.FindPendingOlderThan(ctx, minutes)
	if err != nil {
		return nil, err
	}
	msgs := make([]bird.InboundMessage, 0, len(rows))
	for _, row := range rows {
		if ctx.Err() != nil {
			break // pasada vencida o apagado (D-07): las filas que faltan siguen 'pending' para la siguiente
		}
		if msg, ok := s.replayRow(ctx, row); ok {
			msgs = append(msgs, msg)
		}
	}
	return msgs, nil
}

// replayRow re-despacha UNA fila atascada con el ctx de la fila (utils.RowContext): el plazo de la
// pasada no corta un despacho a Néctar ni su MarkDone a mitad, ni se confunde con Néctar sin
// responder. Devuelve el mensaje de Bird a re-encolar, si lo hay.
func (s inboxReplaySource) replayRow(passCtx context.Context, row localrepo.InboxRow) (bird.InboundMessage, bool) {
	ctx, cancel := utils.RowContext(passCtx)
	defer cancel()
	if api.IsNectarInboxRow(row.MsgType) {
		replayNectarRow(ctx, "stale replay", row, s.nectar, s.repo)
		return bird.InboundMessage{}, false
	}
	var event bird.WebhookEvent
	if err := json.Unmarshal([]byte(row.RawBody), &event); err != nil {
		slog.Error("stale replay: parse failed", "id", row.ID, "error", err)
		_ = s.repo.MarkDone(ctx, row.ID)
		return bird.InboundMessage{}, false
	}
	return bird.ParseInboundMessage(event), true
}

// safeGo runs f in a new goroutine with panic recovery.
// Logs the panic + stack trace via slog so it appears in /api/internal/logs,
// then re-panics so Docker sees a non-zero exit and restarts the container.
func safeGo(name string, f func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error(
					"PANIC in background goroutine",
					"goroutine", name,
					"error", fmt.Sprintf("%v", r),
					"stack", string(debug.Stack()),
				)
				time.Sleep(500 * time.Millisecond) // allow log flush before crash
				panic(r)
			}
		}()
		f()
	}()
}

func debugHandler(localDB, externalDB *sql.DB, pool *worker.MessageWorkerPool, cfg *config.Config, nectarEnabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)

		uptime := time.Since(startTime)

		info := map[string]interface{}{
			"uptime":     uptime.String(),
			"started_at": startTime.Format(time.RFC3339),
			// Qué código corre, no solo desde cuándo. build_commit es el SHA corto con el que se
			// compiló la imagen; comparado contra `git log -1` de main dice si el deploy entró.
			"build_commit":    buildCommit,
			"build_time":      buildTime,
			"goroutines":      runtime.NumGoroutine(),
			"memory_alloc_mb": float64(m.Alloc) / 1024 / 1024,
			"memory_sys_mb":   float64(m.Sys) / 1024 / 1024,
			"memory_heap_mb":  float64(m.HeapAlloc) / 1024 / 1024,
			"gc_cycles":       m.NumGC,
			"gc_last":         time.Since(time.Unix(0, int64(m.LastGC))).String(),
		}

		// Kill switches y perfil: "¿qué versión corre y con qué switches?" tiene que responderse desde
		// aquí. Sin esto había que entrar por SSH a leer el .env — y un switch mal puesto se ve igual
		// que un bug (el bot sano que "no hace nada" es casi siempre BOT_ENABLED=false).
		if cfg != nil {
			info["config"] = map[string]interface{}{
				"bot_enabled":                    cfg.BotEnabled,
				"ai_recovery_enabled":            cfg.AIRecoveryEnabled,
				"whatsapp_notifications_enabled": cfg.WhatsAppNotificationsEnabled,
				"ivr_notifications_enabled":      cfg.IVRNotificationsEnabled,
				"testing_always_open":            cfg.TestingAlwaysOpen,
				"scaling_profile":                cfg.ScalingProfile,
				"log_level":                      cfg.LogLevel,
				"nectar_enabled":                 nectarEnabled,
				"protocol_escalado_version":      cfg.ProtocolEscaladoVersion,
			}
			// Tamaño del log del día: un bucle caliente lo dispara (28-jul-2026: 7,4 GB contra ~30 MB
			// de un día normal). Es la señal más barata de "algo está girando en falso", y además avisa
			// del riesgo de disco. Se mide solo el archivo de hoy, no todo el directorio.
			if cfg.LogDir != "" {
				todayLog := filepath.Join(cfg.LogDir, fmt.Sprintf("neuro-bot-%s.log", time.Now().Format("2006-01-02")))
				if st, err := os.Stat(todayLog); err == nil {
					info["log_today_mb"] = float64(st.Size()) / 1024 / 1024
				}
			}
		}

		// Carga del bot. Tras eliminar /api/internal/kpis/* (motor de KPIs duplicado), estas señales
		// quedaron sin exponer por API y el auditor perdió visibilidad de BACKPRESSURE (cola) y carga.
		// Se reponen aquí (es el endpoint de runtime), NO resucitando el motor de KPIs.
		if pool != nil {
			qsize, qcap := pool.QueueStats()
			info["worker_queue_size"] = qsize
			info["worker_queue_cap"] = qcap
		}
		var activeSessions int
		if err := localDB.QueryRowContext(r.Context(),
			"SELECT COUNT(*) FROM sessions WHERE status = 'active'").Scan(&activeSessions); err == nil {
			info["active_sessions"] = activeSessions
		}

		localStats := localDB.Stats()
		info["local_db"] = map[string]interface{}{
			"open_connections": localStats.OpenConnections,
			"in_use":           localStats.InUse,
			"idle":             localStats.Idle,
			"max_open":         localStats.MaxOpenConnections,
		}

		if externalDB != nil {
			extStats := externalDB.Stats()
			info["external_db"] = map[string]interface{}{
				"open_connections": extStats.OpenConnections,
				"in_use":           extStats.InUse,
				"idle":             extStats.Idle,
				"max_open":         extStats.MaxOpenConnections,
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(info)
	}
}
