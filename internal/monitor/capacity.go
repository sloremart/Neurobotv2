package monitor

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	tg "github.com/neuro-bot/neuro-bot/internal/telegram"
)

const (
	checkInterval = 30 * time.Second // How often to sample metrics

	// Thresholds for scale-up alerts
	queueHighPct    = 80 // Queue fill % to trigger scale-up alert
	dbConnsHighPct  = 80 // DB connection usage % to trigger scale-up alert
	overflowHighPct = 50 // Overflow goroutines % of max (20) to trigger alert

	// Thresholds for scale-down suggestions
	queueLowPct   = 10 // Queue fill % below which scale-down is suggested
	dbConnsLowPct = 20 // DB connection usage % below which scale-down is suggested

	// Cooldowns — avoid spamming Telegram
	alertCooldown    = 15 * time.Minute // Min time between same-type alerts
	scaleDownDelay   = 30 * time.Minute // Must be below threshold for this long before suggesting scale-down
	maxOverflowConst = 20               // matches worker pool maxOverflowGoroutines
)

// QueueStater provides queue fill metrics.
type QueueStater interface {
	QueueStats() (size, capacity int)
}

// CapacityMonitor periodically checks system metrics and sends Telegram alerts
// when the bot approaches capacity limits (scale-up) or is underutilized (scale-down).
type CapacityMonitor struct {
	tgClient   *tg.Client
	workerPool QueueStater
	localDB    *sql.DB
	externalDB *sql.DB
	profile    string // "normal" or "high-load" — informational for messages

	// Config limits (for percentage calculations)
	localDBMaxOpen    int
	externalDBMaxOpen int
	workerCount       int

	// Cooldown tracking
	lastScaleUpAlert   time.Time
	lastScaleDownAlert time.Time
	lowSince           time.Time // When metrics first dropped below low threshold
}

// Config holds the parameters needed to create a CapacityMonitor.
type Config struct {
	TGClient          *tg.Client
	WorkerPool        QueueStater
	LocalDB           *sql.DB
	ExternalDB        *sql.DB
	Profile           string
	LocalDBMaxOpen    int
	ExternalDBMaxOpen int
	WorkerCount       int
}

// New creates a CapacityMonitor. Returns nil if Telegram client is nil.
func New(cfg Config) *CapacityMonitor {
	if cfg.TGClient == nil {
		return nil
	}
	return &CapacityMonitor{
		tgClient:          cfg.TGClient,
		workerPool:        cfg.WorkerPool,
		localDB:           cfg.LocalDB,
		externalDB:        cfg.ExternalDB,
		profile:           cfg.Profile,
		localDBMaxOpen:    cfg.LocalDBMaxOpen,
		externalDBMaxOpen: cfg.ExternalDBMaxOpen,
		workerCount:       cfg.WorkerCount,
	}
}

// Start begins the monitoring loop. Blocks until ctx is cancelled.
func (m *CapacityMonitor) Start(ctx context.Context) {
	if m == nil {
		return
	}
	slog.Info("capacity monitor started",
		"profile", m.profile,
		"check_interval", checkInterval,
		"queue_high_pct", queueHighPct,
		"db_high_pct", dbConnsHighPct,
	)

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.check(ctx)
		}
	}
}

func (m *CapacityMonitor) check(ctx context.Context) {
	now := time.Now()

	// 1. Queue metrics
	queueSize, queueCap := m.workerPool.QueueStats()
	queuePct := 0
	if queueCap > 0 {
		queuePct = (queueSize * 100) / queueCap
	}

	// 2. Local DB connection metrics
	localStats := m.localDB.Stats()
	localPct := 0
	if m.localDBMaxOpen > 0 {
		localPct = (localStats.InUse * 100) / m.localDBMaxOpen
	}

	// 3. External DB connection metrics
	extPct := 0
	if m.externalDB != nil && m.externalDBMaxOpen > 0 {
		extStats := m.externalDB.Stats()
		extPct = (extStats.InUse * 100) / m.externalDBMaxOpen
	}

	slog.Debug("capacity_check",
		"queue_pct", queuePct,
		"queue_size", queueSize,
		"local_db_pct", localPct,
		"local_db_in_use", localStats.InUse,
		"ext_db_pct", extPct,
		"profile", m.profile,
	)

	// Check scale-up conditions
	needsScaleUp := queuePct >= queueHighPct || localPct >= dbConnsHighPct || extPct >= dbConnsHighPct

	if needsScaleUp && now.Sub(m.lastScaleUpAlert) >= alertCooldown {
		m.sendScaleUpAlert(ctx, queuePct, queueSize, queueCap, localPct, localStats.InUse, extPct)
		m.lastScaleUpAlert = now
		m.lowSince = time.Time{} // Reset scale-down timer
		return
	}

	// Check scale-down conditions (only if currently on high-load)
	if m.profile != "high-load" {
		return
	}
	isLow := queuePct <= queueLowPct && localPct <= dbConnsLowPct && extPct <= dbConnsLowPct

	if isLow {
		if m.lowSince.IsZero() {
			m.lowSince = now
		} else if now.Sub(m.lowSince) >= scaleDownDelay && now.Sub(m.lastScaleDownAlert) >= alertCooldown {
			m.sendScaleDownAlert(ctx, queuePct, localPct, extPct)
			m.lastScaleDownAlert = now
		}
	} else {
		m.lowSince = time.Time{} // Reset if metrics rise again
	}
}

func (m *CapacityMonitor) sendScaleUpAlert(ctx context.Context, queuePct, queueSize, queueCap, localPct, localInUse, extPct int) {
	msg := fmt.Sprintf(
		"⚠️ <b>ALERTA CAPACIDAD — Escalar UP</b>\n\n"+
			"Perfil actual: <code>%s</code>\n\n"+
			"📊 <b>Metricas:</b>\n"+
			"• Cola mensajes: <b>%d%%</b> (%d/%d)\n"+
			"• Conexiones BD local: <b>%d%%</b> (%d/%d)\n"+
			"• Conexiones BD externa: <b>%d%%</b>\n\n"+
			"🔧 <b>Accion recomendada:</b>\n"+
			"<code>./scripts/scale-up.sh</code>\n\n"+
			"Workers: 10→50 | Queue: 100→500\n"+
			"DB conns: 25→50 | MySQL max: 50→200",
		m.profile,
		queuePct, queueSize, queueCap,
		localPct, localInUse, m.localDBMaxOpen,
		extPct,
	)

	slog.Warn("capacity alert: scale-up recommended",
		"queue_pct", queuePct,
		"local_db_pct", localPct,
		"ext_db_pct", extPct,
	)

	if err := m.tgClient.SendMessage(ctx, msg); err != nil {
		slog.Error("capacity alert send failed", "error", err)
	}
}

func (m *CapacityMonitor) sendScaleDownAlert(ctx context.Context, queuePct, localPct, extPct int) {
	msg := fmt.Sprintf(
		"📉 <b>Sugerencia — Escalar DOWN</b>\n\n"+
			"Perfil actual: <code>%s</code>\n\n"+
			"📊 <b>Metricas (ultimos 30 min):</b>\n"+
			"• Cola mensajes: <b>%d%%</b>\n"+
			"• Conexiones BD local: <b>%d%%</b>\n"+
			"• Conexiones BD externa: <b>%d%%</b>\n\n"+
			"La carga ha estado baja por 30+ minutos.\n\n"+
			"🔧 <b>Accion sugerida:</b>\n"+
			"<code>./scripts/scale-down.sh</code>\n\n"+
			"Esto libera recursos del servidor.",
		m.profile,
		queuePct,
		localPct,
		extPct,
	)

	slog.Info("capacity suggestion: scale-down",
		"queue_pct", queuePct,
		"local_db_pct", localPct,
		"ext_db_pct", extPct,
	)

	if err := m.tgClient.SendMessage(ctx, msg); err != nil {
		slog.Error("capacity suggestion send failed", "error", err)
	}
}
