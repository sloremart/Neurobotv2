package nectarreports

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/nectar"
	"github.com/neuro-bot/neuro-bot/internal/repository/local"
)

// LA SINCRONIZACIÓN PERIÓDICA DE NÉCTAR (spec D5 §5.1-§5.3).
//
// Cada pasada lee los tramos de espera siguiendo la receta §3(a) del contrato (since = el más
// viejo entre el tramo abierto guardado y la última hora vista − 10 min; ventanas de ≤62 días;
// cursor hasta nulo; upsert por id), lee el roster de asesores y completa las escalaciones
// nectar:* con lo que Néctar sabe de su atención. Nada de esto toca al paciente.

// DataFloor es desde cuándo valen los datos de Néctar para Neuro: nunca se lee antes.
var DataFloor = time.Date(2026, 10, 30, 22, 18, 0, 0, time.UTC)

const (
	// rereadMargin es el margen de relectura de la receta §3(a).
	rereadMargin = 10 * time.Minute
	// maxWindow es el rango máximo que admite GET /api/bot/waiting-cycles.
	maxWindow = 62 * 24 * time.Hour
	// escalationLookback: las escalaciones se buscan desde un día antes del since de los tramos.
	escalationLookback = 24 * time.Hour
)

// Nombres en nectar_sync_state. Los valores de hora van en RFC3339 UTC.
const (
	stateLastSeenOpenedAt = "last_seen_opened_at"
	stateHistorySince     = "history_since"
	stateLastSuccessAt    = "last_success_at"
)

// reportsReader son las dos lecturas de Néctar (nectar.Client).
type reportsReader interface {
	GetWaitingCycles(ctx context.Context, since, until time.Time, cursor string) (nectar.WaitingCyclesPage, error)
	GetAgents(ctx context.Context) ([]nectar.ReportAgent, error)
}

// cyclesStore es la tabla local de tramos, asesores y estado (local.NectarReportsRepo).
type cyclesStore interface {
	UpsertCycles(ctx context.Context, cycles []nectar.WaitingCycle) error
	OldestOpenCycleOpenedAt(ctx context.Context) (*time.Time, error)
	CyclesForConversations(ctx context.Context, convIDs []int64) ([]nectar.WaitingCycle, map[int64]*int64, error)
	SetCycleEscalation(ctx context.Context, cycleIDs []int64, escalationID uint64) error
	ReplaceAgents(ctx context.Context, agents []nectar.ReportAgent) error
	GetState(ctx context.Context, name string) (string, error)
	SetState(ctx context.Context, name, value string) error
}

// escalationsStore son las escalaciones locales de la línea Néctar (local.EscalationRepo).
type escalationsStore interface {
	ListNectarEscalationsSince(ctx context.Context, since time.Time) ([]local.NectarEscalation, error)
	ApplyNectarAttention(ctx context.Context, id uint64, a local.NectarAttentionUpdate) error
}

// Syncer sincroniza tramos y asesores de Néctar y completa las escalaciones nectar:*.
type Syncer struct {
	reader      reportsReader
	cycles      cyclesStore
	escalations escalationsStore
	every       time.Duration
	floor       time.Time
	now         func() time.Time
}

// NewSyncer arma la sincronización; floor es el piso de lectura (DataFloor en producción).
func NewSyncer(r reportsReader, cycles cyclesStore, esc escalationsStore, every time.Duration, floor time.Time) *Syncer {
	return &Syncer{reader: r, cycles: cycles, escalations: esc, every: every, floor: floor.UTC(), now: time.Now}
}

// Run hace una pasada al arrancar y luego una cada `every`, hasta que se cancele ctx. Con la
// clave global (403 account_key_required) deja UN error en el log y termina: no es de reintento.
// Cualquier otro fallo es un aviso y se reintenta en la vuelta siguiente.
func (s *Syncer) Run(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	if !s.pass(ctx) {
		return
	}
	t := time.NewTicker(s.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !s.pass(ctx) {
				return
			}
		}
	}
}

// pass corre una pasada y dice si hay que seguir.
func (s *Syncer) pass(ctx context.Context) bool {
	err := s.RunOnce(ctx)
	switch {
	case err == nil:
		return true
	case errors.Is(err, nectar.ErrAccountKeyRequired):
		slog.Error("informes de Néctar: la sincronización se detiene — waiting-cycles y agents exigen la clave de cuenta "+
			"de la entidad (NECTAR_SERVICE_KEY tiene la clave global)", "error", err)
		return false
	case ctx.Err() != nil:
		return false
	default:
		slog.Warn("informes de Néctar: pasada fallida, se reintenta en la siguiente vuelta", "error", err)
		return true
	}
}

// RunOnce es UNA pasada: tramos, roster, emparejamiento y estado.
func (s *Syncer) RunOnce(ctx context.Context) error {
	now := s.now().UTC()
	since, err := s.since(ctx)
	if err != nil {
		return err
	}

	// 1-2. Tramos, por ventanas y páginas; se guardan página a página.
	lastSeen, historySince, err := s.readCycles(ctx, since, now)
	if err != nil {
		return err
	}
	if lastSeen != nil {
		if err := s.cycles.SetState(ctx, stateLastSeenOpenedAt, formatUTC(*lastSeen)); err != nil {
			return err
		}
	}
	if historySince != nil {
		if err := s.cycles.SetState(ctx, stateHistorySince, formatUTC(*historySince)); err != nil {
			return err
		}
	}

	// 3. Roster.
	agents, err := s.reader.GetAgents(ctx)
	if err != nil {
		return fmt.Errorf("leer asesores de Néctar: %w", err)
	}
	if err := s.cycles.ReplaceAgents(ctx, agents); err != nil {
		return err
	}
	names := make(map[int64]string, len(agents))
	for _, a := range agents {
		names[a.ID] = a.Name
	}

	// 4. Atenciones.
	if err := s.applyAttentions(ctx, since.Add(-escalationLookback), names); err != nil {
		return err
	}

	// 5.
	return s.cycles.SetState(ctx, stateLastSuccessAt, formatUTC(now))
}

// since aplica la receta §3(a): el más viejo entre el tramo abierto guardado y la última hora
// vista menos el margen; sin estado previo, el piso. Nunca antes del piso ni del history_since
// guardado (lo más reciente de los dos es desde cuándo valen los datos).
func (s *Syncer) since(ctx context.Context) (time.Time, error) {
	floor := s.floor
	hs, err := s.stateTime(ctx, stateHistorySince)
	if err != nil {
		return time.Time{}, err
	}
	if hs != nil && hs.After(floor) {
		floor = *hs
	}

	var candidates []time.Time
	oldestOpen, err := s.cycles.OldestOpenCycleOpenedAt(ctx)
	if err != nil {
		return time.Time{}, err
	}
	if oldestOpen != nil {
		candidates = append(candidates, oldestOpen.UTC())
	}
	lastSeen, err := s.stateTime(ctx, stateLastSeenOpenedAt)
	if err != nil {
		return time.Time{}, err
	}
	if lastSeen != nil {
		candidates = append(candidates, lastSeen.Add(-rereadMargin))
	}
	if len(candidates) == 0 {
		return floor, nil
	}
	since := candidates[0]
	for _, c := range candidates[1:] {
		if c.Before(since) {
			since = c
		}
	}
	if since.Before(floor) {
		since = floor
	}
	return since, nil
}

// readCycles lee [since, now) en ventanas de ≤62 días, paginando cada una hasta el cursor nulo, y
// guarda cada página. Devuelve la máxima opened_at vista y el último history_since publicado.
func (s *Syncer) readCycles(ctx context.Context, since, now time.Time) (lastSeen, historySince *time.Time, err error) {
	for start := since; start.Before(now); {
		end := start.Add(maxWindow)
		if end.After(now) {
			end = now
		}
		cursor := ""
		for {
			page, err := s.reader.GetWaitingCycles(ctx, start, end, cursor)
			if err != nil {
				return nil, nil, fmt.Errorf("leer tramos de Néctar: %w", err)
			}
			if err := s.cycles.UpsertCycles(ctx, page.Cycles); err != nil {
				return nil, nil, err
			}
			for _, c := range page.Cycles {
				if lastSeen == nil || c.OpenedAt.After(*lastSeen) {
					t := c.OpenedAt.UTC()
					lastSeen = &t
				}
			}
			if page.HistorySince != nil {
				hs := page.HistorySince.UTC()
				historySince = &hs
			}
			if page.NextCursor == nil || *page.NextCursor == "" {
				break
			}
			cursor = *page.NextCursor
		}
		start = end
	}
	return lastSeen, historySince, nil
}

// applyAttentions empareja las escalaciones nectar:* desde escSince con sus tramos guardados y
// completa cada una con lo que Néctar sabe.
func (s *Syncer) applyAttentions(ctx context.Context, escSince time.Time, names map[int64]string) error {
	list, err := s.escalations.ListNectarEscalationsSince(ctx, escSince)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return nil
	}
	escalations := make([]Escalation, 0, len(list))
	outcomes := make(map[uint64]string, len(list))
	var convIDs []int64
	seenConv := map[int64]bool{}
	for _, e := range list {
		escalations = append(escalations, Escalation{ID: e.ID, Phone: e.Phone, EscalatedAt: e.EscalatedAt})
		if e.Outcome != nil {
			outcomes[e.ID] = *e.Outcome
		}
		if conv, ok := ConversationID(e.Phone); ok && !seenConv[conv] {
			seenConv[conv] = true
			convIDs = append(convIDs, conv)
		}
	}
	if len(convIDs) == 0 {
		return nil
	}
	cycles, marks, err := s.cycles.CyclesForConversations(ctx, convIDs)
	if err != nil {
		return err
	}

	for _, a := range Pair(escalations, cycles) {
		if unmarked := cyclesToMark(a, marks); len(unmarked) > 0 {
			if err := s.cycles.SetCycleEscalation(ctx, unmarked, a.EscalationID); err != nil {
				return err
			}
		}
		if err := s.escalations.ApplyNectarAttention(ctx, a.EscalationID, attentionUpdate(a, names, outcomes[a.EscalationID])); err != nil {
			return err
		}
	}
	return nil
}

// cyclesToMark son los tramos de la atención que todavía no están marcados con ella.
func cyclesToMark(a Attention, marks map[int64]*int64) []int64 {
	var out []int64
	for _, id := range a.CycleIDs {
		if !markedWith(marks[id], a.EscalationID) {
			out = append(out, id)
		}
	}
	return out
}

// markedWith dice si la marca guardada de un tramo es la escalación id (los ids son positivos).
func markedWith(mark *int64, id uint64) bool {
	if mark == nil || *mark < 0 {
		return false
	}
	return uint64(*mark) == id
}

// attentionUpdate traduce una atención a lo que se escribe en la escalación.
func attentionUpdate(a Attention, names map[int64]string, outcome string) local.NectarAttentionUpdate {
	u := local.NectarAttentionUpdate{
		FirstResponseAt: a.FirstResponseAt,
		NoShow:          a.Closed && a.FirstResponseAt == nil && noShowCandidate(outcome),
	}
	if a.AgentID != nil {
		u.AgentID = "nectar:" + strconv.FormatInt(*a.AgentID, 10)
		u.AgentName = names[*a.AgentID] // fuera del roster: vacío, y el repo no pisa con vacío
	}
	// Los mensajes antes de la primera respuesta solo se fijan cuando ya no cambian (hay
	// respuesta o la atención cerró): el repo guarda el primer valor y nunca lo vuelve a pisar.
	if a.FirstResponseAt != nil || a.Closed {
		n := a.ContactMsgs
		u.ContactMsgs = &n
	}
	return u
}

// noShowCandidate: el único cierre que puede ser en realidad «el asesor no contestó» es expired
// (cierre por silencio). completed es un agente que pulsó «Resolver» (mismo significado que en
// Bird); la cola vencida llega como devuelta y la marca el worker. returned y la escalación
// abierta no se tocan.
func noShowCandidate(outcome string) bool {
	return outcome == "expired"
}

// stateTime lee una hora guardada en RFC3339; vacío = nil.
func (s *Syncer) stateTime(ctx context.Context, name string) (*time.Time, error) {
	v, err := s.cycles.GetState(ctx, name)
	if err != nil || v == "" {
		return nil, err
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil, fmt.Errorf("estado %s ilegible %q: %w", name, v, err)
	}
	t = t.UTC()
	return &t, nil
}

func formatUTC(t time.Time) string { return t.UTC().Format(time.RFC3339) }
