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

func main() {
	// Capture signals explicitly so we can log which one we received
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(context.Background())
	safeGo("signal-handler", func() {
		sig := <-sigCh
		slog.Info("signal received", "signal", sig.String())
		cancel()
	})

	cfg := config.Load()
	utils.SetMaskPhones(cfg.LogMaskPhones)

	// Configurar logger
	initLogger(cfg.LogLevel, cfg.LogDir)

	slog.Info(
		"bot starting",
		"pid", os.Getpid(),
		"version", "1.0",
	)

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

	// Configurar timezone
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		log.Fatalf("invalid timezone %s: %v", cfg.Timezone, err)
	}
	time.Local = loc

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

	// Services (dependen de repos)
	var patientSvc *services.PatientService
	var appointmentSvc *services.AppointmentService

	// Build CUPS context for OCR prompt (reference table for AI matching)
	cupsContext := ""
	if repos != nil && repos.Procedure != nil {
		if allProcs, err := repos.Procedure.FindAllActive(ctx); err == nil {
			entries := make([]struct{ Code, Name string }, len(allProcs))
			for i, p := range allProcs {
				entries[i] = struct{ Code, Name string }{p.Code, p.Name}
			}
			cupsContext = services.BuildCupsContext(entries)
			slog.Info("cups context loaded", "procedures", len(allProcs))
		} else {
			slog.Warn("failed to load cups context for OCR", "error", err)
		}
	}
	ocrSvc := services.NewOCRService(cfg.OpenAIAPIKey, cfg.OpenAIModel, cupsContext, cfg.BirdAccessKeyID)

	if repos != nil {
		patientSvc = services.NewPatientService(repos.Patient)
		appointmentSvc = services.NewAppointmentService(repos.Appointment, cfg)
		appointmentSvc.SetConfirmationLog(localrepo.NewConfirmationLogRepo(localDB))
	}

	// State machine (interceptores + handlers se registran por fase)
	machine := statemachine.NewMachine()
	statemachine.SetMaxRetries(cfg.MaxRetries)
	statemachine.RegisterInterceptors(machine)

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
		handlers.RegisterAppointmentHandlers(machine, appointmentSvc, procRepoForAppts, addrMapper, onCancel)
	}
	// Fase 8: Orden Médica y OCR
	waitingListRepo := localrepo.NewWaitingListRepo(localDB)
	if repos != nil {
		handlers.RegisterMedicalOrderHandlers(machine, ocrSvc, repos.Procedure, birdClient, waitingListRepo, appointmentSvc)
	}
	// Fase 9: Validaciones Médicas
	gfrSvc := services.NewGFRService()
	if appointmentSvc != nil {
		handlers.RegisterMedicalValidationHandlers(machine, gfrSvc, appointmentSvc)
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
	}

	// Fase 14: Event Tracking
	eventRepo := localrepo.NewEventRepo(localDB)
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
	}

	// Message inbox (WAL for crash recovery)
	inboxRepo := localrepo.NewInboxRepo(localDB)

	// Worker pool (configurable via WORKER_POOL_SIZE / WORKER_QUEUE_SIZE)
	workerPool := worker.NewMessageWorkerPool(cfg.WorkerPoolSize, cfg.WorkerQueueSize)
	workerPool.SetDependencies(sessionManager, birdClient, machine)
	workerPool.SetTracker(tracker)
	workerPool.SetAgentActivityChecker(escalationRepo) // acuse durante escalación (§8.1 #6)
	workerPool.SetOCRService(ocrSvc)
	workerPool.SetInboxRepo(inboxRepo)
	workerPool.SetBotEnabled(cfg.BotEnabled)
	if notifyManager != nil {
		workerPool.SetNotifyResponder(notifyManager)
	}
	workerPool.Start(ctx)

	// Fase 20: Inactivity checker (single reminder + silent close for active, expire for escalated).
	// Va DESPUÉS del worker pool porque le inyecta el Resumer (workerPool) para el no-show de escalación
	// (devolver al bot las escalaciones que el agente nunca atendió — necesita la state machine del worker).
	safeGo("inactivity-checker", func() {
		sessionManager.StartInactivityChecker(ctx, session.InactivityDeps{
			BirdClient:         birdClient,
			Tracker:            tracker,
			ReminderMin:        cfg.InactivityReminderMin,
			CloseMin:           cfg.InactivityCloseMin,
			EscalationCloseMin: cfg.EscalationPatientCloseMin,
			AgentReminderMin:   cfg.EscalationAgentReminderMin,
			AgentReminderMax:   cfg.EscalationAgentReminderMax,
			Resumer:            workerPool,
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

	// WAL replay: re-process messages that weren't completed before last shutdown/crash
	if pending, err := inboxRepo.FindPending(ctx); err != nil {
		slog.Error("inbox replay query failed", "error", err)
	} else if len(pending) > 0 {
		slog.Info("replaying unprocessed inbox messages", "count", len(pending))
		for _, row := range pending {
			var event bird.WebhookEvent
			if err := json.Unmarshal([]byte(row.RawBody), &event); err != nil {
				slog.Error("inbox replay parse failed", "id", row.ID, "error", err)
				inboxRepo.MarkDone(ctx, row.ID)
				continue
			}
			msg := bird.ParseInboundMessage(event)

			// Classify: notification postback vs regular message
			if notifyManager != nil && notifyManager.HasPending(msg.Phone) {
				if msg.IsPostback && api.IsNotificationPostback(msg.PostbackPayload) {
					slog.Info("WAL replay: notification postback routed to handler",
						"id", row.ID, "phone", msg.Phone, "payload", msg.PostbackPayload)
					go notifyManager.HandleResponse(msg.Phone, msg.PostbackPayload, msg.ConversationID)
					inboxRepo.MarkDone(ctx, row.ID)
					continue
				}
				if notifyManager.HandleInvalidInput(msg.Phone, msg.ConversationID) {
					slog.Info("WAL replay: invalid input during notification handled",
						"id", row.ID, "phone", msg.Phone)
					inboxRepo.MarkDone(ctx, row.ID)
					continue
				}
			}

			workerPool.Enqueue(msg)
		}
	}

	// M7: re-replay periódico de mensajes 'pending' atascados (p.ej. descartados por backpressure),
	// para recuperarlos sin esperar al reinicio del proceso.
	safeGo("stale-replay", func() {
		workerPool.StartStaleReplay(ctx, inboxReplaySource{repo: inboxRepo})
	})

	// Fase 13: Inyectar dependencias de lista de espera al NotificationManager
	if notifyManager != nil {
		notifyManager.SetWaitingListDeps(waitingListRepo, sessionRepo, workerPool)
	}

	// Cambio 13: Inyectar dependencias para WL check en tiempo real
	if notifyManager != nil && slotSvc != nil && repos != nil {
		notifyManager.SetWaitingListCheckDeps(slotSvc, repos.Appointment, waitingListRepo, repos.Schedule, repos.Procedure)
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
			NotifHistory:    eventRepo, // dedup del recordatorio de corta antelación (fail-closed si nil)
		}
		// Reconciliación de invariantes (Fase 2 observabilidad): checks locales + SIESA (bot-filtered).
		reconciler := observability.NewReconciler()
		reconciler.Register("wl_stuck", observability.StuckWaitingListCheck(localDB, 25))
		reconciler.Register("zombie_escalated", observability.ZombieEscalatedCheck(localDB, 1))
		if externalDB != nil {
			reconciler.Register("orphan_slot", observability.OrphanSlotCheck(externalDB, 4, cfg.SIESAAssignUserCedula))
			reconciler.Register("consulta_valor_cero", observability.ConsultaValorCeroCheck(externalDB, 4, cfg.SIESAAssignUserCedula))
		}
		schedulerTasks.Reconciler = reconciler
		schedulerTasks.FlowMaint = flowRepo // rollup + purga de flow_events (Fase 3)

		schedulerTasks.RegisterAll(sched)
		sched.RunMissedTasks(ctx) // Catch-up missed tasks before starting the regular loop
		safeGo("scheduler", func() { sched.Start(ctx) })
	}

	// Webhook handler (con NotificationManager para postbacks proactivos + WAL inbox)
	webhookHandler := api.NewWebhookHandler(birdClient, workerPool, notifyManager, cfg)
	webhookHandler.SetInboxRepo(inboxRepo)
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
				internalHandler.SetSiesaAnalyticsReader(siesa.NewAnalyticsRepo(externalDB, 10*time.Minute), repos.Procedure)
			}
		}
	}

	// HTTP Server
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", healthHandler(localDB, externalDB))
	mux.Handle("GET /health/debug", api.InternalAuth(cfg.InternalAPIKey)(http.HandlerFunc(debugHandler(localDB, externalDB, workerPool))))
	mux.HandleFunc("POST /api/webhooks/whatsapp", webhookHandler.HandleWhatsApp)
	mux.HandleFunc("POST /api/webhooks/whatsapp/outbound", webhookHandler.HandleWhatsAppOutbound)
	mux.HandleFunc("POST /api/webhooks/conversations", webhookHandler.HandleConversation)
	mux.HandleFunc("POST /api/webhooks/voice", webhookHandler.HandleVoiceWebhook)
	mux.HandleFunc("POST /api/webhooks/voice/dtmf", webhookHandler.HandleVoiceDTMF)

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
		internalMux.HandleFunc("GET /api/internal/siesa/conversion", internalHandler.HandleSiesaConversion)
		internalMux.HandleFunc("GET /api/internal/siesa/bot-share", internalHandler.HandleSiesaBotShare)
		internalMux.HandleFunc("GET /api/internal/siesa/conciliacion", internalHandler.HandleSiesaConciliacion)
		mux.Handle(
			"/api/internal/",
			api.RateLimiter(30, time.Minute)(
				api.MaxBodySize(
					api.InternalAuth(cfg.InternalAPIKey)(internalMux),
				),
			),
		)
	}

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

	// 2. Wait for worker pool goroutines to finish (before DB close via defers)
	stopDone := make(chan struct{})
	go func() {
		workerPool.Stop()
		close(stopDone)
	}()

	select {
	case <-stopDone:
		slog.Info("shutdown complete")
	case <-time.After(20 * time.Second):
		slog.Error("shutdown timeout: worker pool did not stop within 20s")
	}

	// 3. Drenar tareas del scheduler en vuelo (N-37) antes de cerrar conexiones (DB se cierra en
	// los defers al retornar). El ctx ya fue cancelado, así que las tareas paran rápido (N-38);
	// las que terminan persisten su last-run para no re-ejecutarse en el arranque.
	if sched != nil {
		sched.Wait(20 * time.Second)
	}

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

func initLogger(level, logDir string) {
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
		fw, err := logging.NewDailyFileWriter(logDir, "neuro-bot", 30)
		if err != nil {
			log.Printf("WARN: could not init log file writer: %v (logging to stdout only)", err)
		} else {
			w = io.MultiWriter(os.Stdout, fw)
		}
	}

	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: logLevel,
		// K3: redacción global de PII (Ley 1581) en TODOS los logs (stdout/archivo). Las claves
		// sensibles (documento/nombre/teléfono) se enmascaran antes de escribirse. El handler de
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
			Price:        siesa.NewPriceRepo(externalDB),
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

func healthHandler(localDB, externalDB *sql.DB) http.HandlerFunc {
	cache := &healthCache{}

	return func(w http.ResponseWriter, r *http.Request) {
		cache.mu.Lock()
		// N-47: si el resultado cacheado sigue vigente, responderlo sin Ping.
		if cache.health == nil || time.Since(cache.lastCheck) >= healthCacheTTL {
			health := map[string]string{"status": "ok"}
			critical := false

			pingCtx, cancelPing := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancelPing()

			if err := localDB.PingContext(pingCtx); err != nil {
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
			} else if err := externalDB.PingContext(pingCtx); err != nil {
				health["external_db"] = "error"
				if health["status"] == "ok" {
					health["status"] = "degraded"
				}
			} else {
				health["external_db"] = "ok"
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
	repo *localrepo.InboxRepo
}

func (s inboxReplaySource) PendingOlderThan(ctx context.Context, minutes int) ([]bird.InboundMessage, error) {
	rows, err := s.repo.FindPendingOlderThan(ctx, minutes)
	if err != nil {
		return nil, err
	}
	msgs := make([]bird.InboundMessage, 0, len(rows))
	for _, row := range rows {
		var event bird.WebhookEvent
		if err := json.Unmarshal([]byte(row.RawBody), &event); err != nil {
			slog.Error("stale replay: parse failed", "id", row.ID, "error", err)
			_ = s.repo.MarkDone(ctx, row.ID)
			continue
		}
		msgs = append(msgs, bird.ParseInboundMessage(event))
	}
	return msgs, nil
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

func debugHandler(localDB, externalDB *sql.DB, pool *worker.MessageWorkerPool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)

		uptime := time.Since(startTime)

		info := map[string]interface{}{
			"uptime":          uptime.String(),
			"started_at":      startTime.Format(time.RFC3339),
			"goroutines":      runtime.NumGoroutine(),
			"memory_alloc_mb": float64(m.Alloc) / 1024 / 1024,
			"memory_sys_mb":   float64(m.Sys) / 1024 / 1024,
			"memory_heap_mb":  float64(m.HeapAlloc) / 1024 / 1024,
			"gc_cycles":       m.NumGC,
			"gc_last":         time.Since(time.Unix(0, int64(m.LastGC))).String(),
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
