package observability

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/domain"
)

type fakeSink struct {
	mu     sync.Mutex
	events []domain.FlowEvent
}

func (f *fakeSink) InsertBatch(_ context.Context, evs []domain.FlowEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, evs...)
	return nil
}

func (f *fakeSink) all() []domain.FlowEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.FlowEvent(nil), f.events...)
}

// runEmits corre un Tracer al nivel dado, ejecuta los emits y drena en el apagado (determinista).
func runEmits(maxLvl Level, emits func(tr *Tracer)) []domain.FlowEvent {
	sink := &fakeSink{}
	tr := New(sink, maxLvl)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		tr.Start(ctx)
		close(done)
	}()
	emits(tr)
	cancel()
	<-done
	return sink.all()
}

// TestTracer_LevelGate verifica que FLOW_TRACE_LEVEL filtra acumulativamente.
func TestTracer_LevelGate(t *testing.T) {
	do := func(tr *Tracer) {
		tr.emit("wl:1", "lista_espera", "notify_failed", EmitOpts{}) // error (1)
		tr.emit("wl:1", "lista_espera", "booked", EmitOpts{})        // outcome (2)
		tr.emit("wl:1", "lista_espera", "slot_match", EmitOpts{})    // milestone (3)
		tr.emit("wl:1", "lista_espera", "skipped", EmitOpts{})       // full (4)
	}
	cases := []struct {
		lvl  Level
		want int
	}{
		{LvOff, 0}, {LvError, 1}, {LvOutcome, 2}, {LvMilestone, 3}, {LvFull, 4},
	}
	for _, c := range cases {
		if got := runEmits(c.lvl, do); len(got) != c.want {
			t.Errorf("level %d: got %d events, want %d", c.lvl, len(got), c.want)
		}
	}
}

// TestTracer_WaitingListJourneyOrdered es el criterio de aceptación de la Fase 0: el recorrido
// completo de una lista de espera queda registrado, ordenado y bajo el mismo trace_id.
func TestTracer_WaitingListJourneyOrdered(t *testing.T) {
	got := runEmits(LvMilestone, func(tr *Tracer) {
		tr.emit("wl:7", "lista_espera", "enrolled", EmitOpts{Phone: "+573001234567"})
		tr.emit("wl:7", "lista_espera", "slot_match", EmitOpts{})
		tr.emit("wl:7", "lista_espera", "notified", EmitOpts{RefID: "bird-1"})
		tr.emit("wl:7", "lista_espera", "response_schedule", EmitOpts{})
		tr.emit("wl:7", "lista_espera", "booked", EmitOpts{RefID: "cita-9"})
	})
	want := []string{"enrolled", "slot_match", "notified", "response_schedule", "booked"}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d", len(got), len(want))
	}
	for i, s := range want {
		if got[i].Step != s {
			t.Errorf("event %d: got %q want %q", i, got[i].Step, s)
		}
		if got[i].TraceID != "wl:7" {
			t.Errorf("event %d trace_id = %q, want wl:7", i, got[i].TraceID)
		}
	}
	// ref_id de booked debe ser la cita (pivote entre trazas)
	if got[4].RefID != "cita-9" || got[4].RefType != "cita" {
		t.Errorf("booked ref = %q/%q, want cita-9/cita", got[4].RefID, got[4].RefType)
	}
}

func TestTracer_MasksPhone(t *testing.T) {
	got := runEmits(LvMilestone, func(tr *Tracer) {
		tr.emit("wl:1", "lista_espera", "notified", EmitOpts{Phone: "+573001234567"})
	})
	if len(got) != 1 {
		t.Fatalf("want 1 event, got %d", len(got))
	}
	if got[0].Phone == "+573001234567" || got[0].Phone == "" {
		t.Errorf("phone not masked: %q", got[0].Phone)
	}
}

func TestSanitizeAttrs_DropsPII(t *testing.T) {
	out := sanitizeAttrs(map[string]interface{}{
		"cups": "890274", "espacios": 2, "nombre": "Juan", "documento": "123", "direccion": "calle 1",
	})
	if _, ok := out["cups"]; !ok {
		t.Error("cups (permitida) debió pasar")
	}
	if _, ok := out["espacios"]; !ok {
		t.Error("espacios (permitida) debió pasar")
	}
	for _, bad := range []string{"nombre", "documento", "direccion"} {
		if _, ok := out[bad]; ok {
			t.Errorf("%q (PII) NO debió pasar", bad)
		}
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]Level{
		"off": LvOff, "error": LvError, "outcome": LvOutcome,
		"milestone": LvMilestone, "full": LvFull, "": LvMilestone, "basura": LvMilestone,
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %d, want %d", in, got, want)
		}
	}
}

// TestCatalog_AllEmittedStepsRegistered: todos los steps que el código emite tienen entrada en el
// catálogo (impide desincronización código↔§3A). Listar aquí cada step instrumentado.
func TestCatalog_AllEmittedStepsRegistered(t *testing.T) {
	emitted := map[string][]string{
		"lista_espera": {
			"enrolled", "slot_match", "skipped", "claim_lost", "duplicate_found",
			"notified", "notify_failed", "response_schedule", "declined", "booked", "expired",
		},
		"agendar": {
			"ocr_ok", "ocr_failed", "cups_none", "gfr_blocked", "pregnancy_blocked",
			"special_escalated", "already_has_appt", "coverage_particular", "coverage_escalated",
			"slots_found", "no_slots", "booking_success", "booking_failed", "reschedule_orphan",
		},
		"notif_recordatorio": {
			"reminder_sent", "ivr_placed", "confirmed", "cancelled", "escalated", "error",
		},
		"identificacion": {"patient_lookup", "contact_updated", "lookup_failed"},
		"entidad": {
			"entity_selected", "contract_resolved", "sanitas_muni_forced",
			"no_entities", "entity_update_failed",
		},
		"registro": {
			"registration_started", "patient_created", "registration_abandoned", "patient_create_failed",
		},
		"mis_citas": {
			"appointments_listed", "appt_confirmed", "appt_cancelled", "reschedule_started", "no_appointments",
		},
		"escalacion": {
			"escalated", "agent_resumed", "agent_closed", "agent_reminder_sent", "escalation_expired",
			"agent_no_show", "escalation_no_channel",
		},
		"recuperacion": {
			"ai_recovery_started", "ai_recovered", "ai_resolved_by_bot", "ai_clarified",
			"ai_failed", "ai_domain_block", "ai_month_cap_reached",
		},
		"scheduler": {"task_completed", "task_failed"},
		"admin_agenda": {
			"agenda_cancelled", "agenda_rescheduled", "patients_notified",
		},
		"infra": {"session_abandoned", "phone_lock_timeout"},
	}
	for flow, steps := range emitted {
		for _, s := range steps {
			if _, ok := catalog[flow+"/"+s]; !ok {
				t.Errorf("catalog sin entrada para %s/%s", flow, s)
			}
		}
	}
}

// TestCatalog_Classification verifica que los hitos clave salen con el nivel y outcome correctos
// (criterio de aceptación Fase 1: gfr_blocked→blocked, ocr_failed→error son consultables por tipo).
func TestCatalog_Classification(t *testing.T) {
	got := runEmits(LvFull, func(tr *Tracer) {
		tr.emit("sess:1", "agendar", "gfr_blocked", EmitOpts{})
		tr.emit("sess:1", "agendar", "ocr_failed", EmitOpts{})
		tr.emit("sess:1", "agendar", "booking_success", EmitOpts{RefID: "c1"})
		tr.emit("notif:1", "notif_recordatorio", "confirmed", EmitOpts{})
		tr.emit("notif:1", "notif_recordatorio", "error", EmitOpts{})
	})
	want := map[string]struct {
		level   int
		outcome string
	}{
		"gfr_blocked":     {int(LvOutcome), "blocked"},
		"ocr_failed":      {int(LvError), "error"},
		"booking_success": {int(LvOutcome), "ok"},
		"confirmed":       {int(LvOutcome), "ok"},
		"error":           {int(LvError), "error"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d", len(got), len(want))
	}
	for _, e := range got {
		w, ok := want[e.Step]
		if !ok {
			t.Errorf("step inesperado %q", e.Step)
			continue
		}
		if e.Level != w.level || e.Outcome != w.outcome {
			t.Errorf("%s: level=%d outcome=%q, want level=%d outcome=%q", e.Step, e.Level, e.Outcome, w.level, w.outcome)
		}
	}
}

// TestTruncateReason (M6): reason se acota a 60 chars y sin saltos de línea (no rompe el VARCHAR(60)).
func TestTruncateReason(t *testing.T) {
	if got := truncateReason("ok"); got != "ok" {
		t.Errorf("corto sin cambios, got %q", got)
	}
	long := "mssql: Violation of PRIMARY KEY constraint en una tabla con un nombre larguísimo y detalle"
	if got := truncateReason(long); len([]rune(got)) != maxReasonLen {
		t.Errorf("esperaba %d runas, got %d (%q)", maxReasonLen, len([]rune(got)), got)
	}
	if got := truncateReason("línea1\nlínea2"); got != "línea1 línea2" {
		t.Errorf("debe quitar saltos de línea, got %q", got)
	}
}

// La allowlist de attrs es un filtro SILENCIOSO: una clave no listada se descarta sin error ni aviso.
// Eso convirtió el attr `stashed` en un punto ciego — se emitía en cada rechazo de imagen pero nunca
// llegaba a la tabla, así que no se podía distinguir el callejón real (la foto se perdió) de la
// recuperación (quedó guardada). Este test fija las claves de las que depende la auditoría.
func TestSanitizeAttrs_KeepsAuditKeys(t *testing.T) {
	out := sanitizeAttrs(map[string]interface{}{
		"state":   "ASK_CLIENT_TYPE",
		"stashed": false, // el valor falso también debe conservarse: es el caso que delata el callejón
	})
	if got, ok := out["state"]; !ok || got != "ASK_CLIENT_TYPE" {
		t.Errorf("state debió pasar, got %v (ok=%v)", got, ok)
	}
	got, ok := out["stashed"]
	if !ok {
		t.Fatal("stashed debió pasar: sin él, el evento no distingue rechazo de recuperación")
	}
	if got != false {
		t.Errorf("stashed debió conservar su valor false, got %v", got)
	}
}

// H130-2: `stash_reason` explica por qué la foto no se guardó (no_media / already_read /
// already_stashed). Es la clave que permite separar "ya tenía su orden" de "no llegó media" sin salir
// de la API; si no está en la allowlist, sanitizeAttrs la descarta EN SILENCIO y la auditoría vuelve a
// inferir en vez de leer (ciclo 129: esa ceguera produjo una conclusión errónea sobre un despliegue).
func TestSanitizeAttrs_KeepsStashReason(t *testing.T) {
	out := sanitizeAttrs(map[string]interface{}{
		"stashed":      false,
		"stash_reason": "already_stashed",
	})
	if got, ok := out["stash_reason"]; !ok || got != "already_stashed" {
		t.Errorf("stash_reason debió pasar, got %v (ok=%v)", got, ok)
	}
}

// TestCatalog_CoversEveryEmitInTree recorre el árbol y exige que TODO (flow, step) que el código emite
// esté registrado en el catálogo.
//
// Por qué existe: un Emit sin entrada en el catálogo NO falla — emit() lo acepta con un WARN y le pone
// level=milestone/outcome=info por defecto. Consecuencia silenciosa: un step TERMINAL registrado como
// milestone hace que su trace nunca se dé por cerrado (el detector de completitud lo reporta
// "estancado" para siempre) y un bloqueo se contabiliza como 'info'. La auditoría del 2026-08-01
// encontró DIEZ steps así, acumulados por varios fixes que agregaron su Emit sin tocar el catálogo:
// nada lo impedía, porque el aviso es un WARN que nadie mira. Este test es lo que lo impide.
func TestCatalog_CoversEveryEmitInTree(t *testing.T) {
	root := filepath.Join("..", "..")
	// Se recorre el AST, no el texto: una versión con regex dejaba fuera 5 call sites por detalles de
	// sintaxis (traceID literal, o una llamada con coma/cadena dentro del argumento, como
	// agendaTrace(id, fecha) o appt.Date.Format("2006-01-02")). Justo el tipo de punto ciego que este
	// test existe para no tener.
	lit := func(e ast.Expr) (string, bool) {
		b, ok := e.(*ast.BasicLit)
		if !ok || b.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(b.Value)
		return s, err == nil
	}

	missing := map[string][]string{}
	callSites, parsed := 0, 0
	var dynamic, unreadable []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		// Un archivo que no se puede leer o parsear NO se salta en silencio: se acumula y se reporta al
		// final. Saltarlo callado dejaría Emit sin verificar y el test diría "todo bien" sin haberlo mirado.
		if walkErr != nil {
			unreadable = append(unreadable, path+": "+walkErr.Error())
			return nil //nolint:nilerr // se reporta al final; abortar el Walk ocultaría el resto del árbol
		}
		// Saltar directorios ocultos (.git, .claude, …). El equipo usa git worktrees dentro de
		// .claude/worktrees/, que son COPIAS COMPLETAS del repo: sin esto el Walk cuenta cada archivo
		// dos veces y el test falla con "Emit con step dinámico en 2 archivos" — un rojo espurio que
		// depende de si tienes un worktree abierto, no del código.
		if d.IsDir() && path != root && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			unreadable = append(unreadable, path+": "+perr.Error())
			return nil //nolint:nilerr // ídem: se acumula y se reporta, no se ignora
		}
		fileHasDynamic := false
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Emit" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "observability" {
				return true
			}
			callSites++
			if len(call.Args) < 3 {
				fileHasDynamic = true
				return true
			}
			flow, okF := lit(call.Args[1])
			step, okS := lit(call.Args[2])
			if !okF || !okS {
				fileHasDynamic = true // step (o flow) en variable: no verificable estáticamente
				return true
			}
			parsed++
			key := flow + "/" + step
			if _, ok := catalog[key]; !ok {
				missing[key] = append(missing[key], path)
			}
			return true
		})
		if fileHasDynamic {
			dynamic = append(dynamic, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("recorrer el árbol: %v", err)
	}

	// Los Emit con step en VARIABLE no se pueden verificar leyendo el fuente. Hoy hay exactamente uno
	// (internal/recovery/coordinator.go, que emite los 7 pasos de 'recuperacion', todos catalogados).
	// Se fija ese número: si aparece otro, este test falla a propósito para obligar a comprobar a mano
	// que sus steps estén en el catálogo. Un test que ignora en silencio lo que no puede leer repite el
	// mismo defecto que este test existe para evitar.
	const knownDynamicEmitFiles = 1
	if len(dynamic) != knownDynamicEmitFiles {
		t.Errorf("Emit con step dinámico en %d archivo(s), esperaba %d: %v\n"+
			"  → verificá a mano que todos los steps posibles estén en el catálogo y actualizá esta constante",
			len(dynamic), knownDynamicEmitFiles, dynamic)
	}
	if len(unreadable) > 0 {
		t.Errorf("%d archivo(s) no se pudieron leer/parsear, así que sus Emit quedaron SIN verificar: %v",
			len(unreadable), unreadable)
	}
	if callSites == 0 {
		t.Fatal("no se encontró ningún observability.Emit( — el recorrido del árbol no funcionó")
	}
	t.Logf("verificados %d de %d call sites de Emit (%d dinámico(s))", parsed, callSites, callSites-parsed)

	if len(missing) > 0 {
		keys := make([]string, 0, len(missing))
		for k := range missing {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		t.Errorf("%d (flow, step) emitidos SIN entrada en el catálogo — quedarían como milestone/info "+
			"por defecto (terminal que no cierra, bloqueo contado como info):", len(keys))
		for _, k := range keys {
			t.Errorf("  %s  ← %s", k, missing[k][0])
		}
	}
}

// TestCatalog_NewTerminalsAreTerminal fija el criterio que motivó H131-1: no basta con que el step ESTÉ
// en el catálogo, tiene que estar con el nivel correcto. Un terminal registrado como milestone deja su
// trace abierto para siempre y el detector de completitud lo reporta "estancado" en cada ciclo; un
// bloqueo registrado como 'info' no aparece en las estadísticas de bloqueos. La presencia la garantiza
// TestCatalog_CoversEveryEmitInTree; el NIVEL lo garantiza este.
func TestCatalog_NewTerminalsAreTerminal(t *testing.T) {
	terminals := map[string]string{
		"notif_recordatorio/same_day_no_response":       "info",      // avisado y sin respuesta: nada más que hacer
		"notif_recordatorio/escalation_no_conversation": "error",     // el handoff falló: nadie avisó al agente
		"escalacion/escalation_no_agents":               "blocked",   // gate cierra el intento y devuelve al menú
		"agendar/ocr_no_cups_code":                      "escalated", // sin CUPS válidos → agente
		"agendar/bloqueo_sanitas_escalated":             "escalated", // CUP de bloqueo → agente
	}
	for key, wantOutcome := range terminals {
		spec, ok := catalog[key]
		if !ok {
			t.Errorf("%s: sin entrada en el catálogo", key)
			continue
		}
		if spec.level > LvOutcome {
			t.Errorf("%s: level=%d (milestone o más verboso) — es TERMINAL, su trace nunca cerraría",
				key, spec.level)
		}
		if spec.outcome != wantOutcome {
			t.Errorf("%s: outcome=%q, esperaba %q", key, spec.outcome, wantOutcome)
		}
	}
}
