//go:build seed

// Command seed-kpis llena las tablas de analítica de la BD local del bot (neuro_bot) con datos
// DERIVADOS de las citas reales de SIESA + flujos sintéticos, para validar todos los KPIs del
// dashboard. SOLO desarrollo: no entra al binario del server. Exige --yes y aborta si el DSN
// apunta a producción.
//
// Uso (vía contenedor golang, ya que go no está en el host):
//
//	LOCAL_DSN=... SIESA_DSN=... go run ./cmd/seed-kpis --yes
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/config"

	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	_ "github.com/microsoft/go-mssqldb"
)

var now = time.Now()

// asuntoService traduce el asunto SIESA al nombre de servicio (para el JSON service_type).
var asuntoService = map[int]string{
	1: "Consulta Fisiatria", 2: "RX", 3: "Tomografia", 4: "Resonancia", 5: "Mamografia",
	6: "Ecografia", 7: "Consulta Fisiatria", 8: "Consulta Neurologia", 9: "Consulta Neurologia",
	10: "Consulta Neuropediatria", 11: "Consulta Neuropediatria", 12: "PET/CT", 13: "Polisomnografia",
	14: "EEG", 15: "Procedimiento Fisiatria", 16: "Procedimiento Neurologia", 17: "Soporte Sedacion",
	19: "Doppler",
}

var entities = []string{
	"SANITAS EPS", "SALUD TOTAL", "NUEVA EPS", "CAPITAL SALUD", "FOMAG", "COLSANITAS",
	"MEDISANITAS", "PARTICULAR", "COOSALUD", "FAMISANAR",
}

var escalationStates = []string{
	"ASK_ENTITY_NUMBER", "UPLOAD_MEDICAL_ORDER", "SHOW_SLOTS", "CONFIRM_OCR_RESULT",
	"ASK_DOCUMENT_TYPE", "REG_BIRTH_DATE", "ASK_MANUAL_CUPS", "GFR_CREATININE", "MAIN_MENU",
}

func main() {
	days := flag.Int("days", 45, "ventana pasada en días (citas y eventos)")
	ahead := flag.Int("ahead", 30, "ventana futura (solo informativo; SIESA ya tiene las próximas)")
	leakRatio := flag.Float64("leak-ratio", 0.6, "sesiones sin cita / sesiones con cita")
	yes := flag.Bool("yes", false, "confirmación obligatoria para truncar y sembrar")
	listAgents := flag.Bool("list-agents", false, "solo lista los agentes reales de Bird y sale (no toca la BD)")
	flag.Parse()
	_ = *ahead

	if *listAgents {
		ags := loadBirdAgents()
		fmt.Printf("== %d agentes ==\n", len(ags))
		for _, a := range ags {
			fmt.Printf("  %-40s %s\n", a.id, a.name)
		}
		return
	}

	if !*yes {
		log.Fatal("abortado: pasa --yes para confirmar (esto trunca las tablas de analítica)")
	}
	localDSN := os.Getenv("LOCAL_DSN")
	siesaDSN := os.Getenv("SIESA_DSN")
	if localDSN == "" || siesaDSN == "" {
		log.Fatal("faltan env LOCAL_DSN y/o SIESA_DSN")
	}
	if strings.Contains(localDSN, "192.168.1.207") || strings.Contains(strings.ToLower(localDSN), "prod") {
		log.Fatal("abortado: el LOCAL_DSN parece de producción")
	}

	ldb, err := sql.Open("mysql", localDSN)
	must(err, "open local")
	defer ldb.Close()
	must(ldb.Ping(), "ping local")

	sdb, err := sql.Open("sqlserver", siesaDSN)
	must(err, "open siesa")
	defer sdb.Close()
	must(sdb.Ping(), "ping siesa")

	fmt.Println("== seed-kpis ==")
	catalog := loadCatalog(ldb)
	fmt.Printf("catálogo local: %d CUPS\n", len(catalog))

	truncate(ldb)

	s := &summary{counts: map[string]int{}}
	seedFromCitas(ldb, sdb, catalog, *days, s)
	seedSynthetic(ldb, catalog, *days, *leakRatio, s)

	s.print()
	fmt.Println("== listo ==")
}

// ---------------------------------------------------------------------------
// Infra
// ---------------------------------------------------------------------------

func must(err error, ctx string) {
	if err != nil {
		log.Fatalf("%s: %v", ctx, err)
	}
}

type cup struct {
	code, name string
	asunto     int
}

func loadCatalog(db *sql.DB) []cup {
	rows, err := db.Query(`SELECT codigo_cups, nombre, asunto_id FROM cups_procedimientos WHERE activo=1`)
	must(err, "load catalog")
	defer rows.Close()
	var out []cup
	for rows.Next() {
		var c cup
		must(rows.Scan(&c.code, &c.name, &c.asunto), "scan cup")
		out = append(out, c)
	}
	return out
}

var analyticsTables = []string{
	"chat_events", "flow_events", "flow_daily_stats", "session_context", "sessions",
	"waiting_list", "notification_pending", "notification_history", "confirmation_log",
	"communication_messages", "communication_calls", "message_inbox",
}

func truncate(db *sql.DB) {
	_, _ = db.Exec("SET FOREIGN_KEY_CHECKS=0")
	for _, t := range analyticsTables {
		if _, err := db.Exec("TRUNCATE TABLE " + t); err != nil {
			log.Printf("warn truncate %s: %v", t, err)
		}
	}
	_, _ = db.Exec("SET FOREIGN_KEY_CHECKS=1")
	fmt.Printf("truncadas %d tablas de analítica\n", len(analyticsTables))
}

// batch acumula filas y las inserta en lotes.
type batch struct {
	db    *sql.DB
	table string
	cols  []string
	rows  [][]any
}

func newBatch(db *sql.DB, table string, cols ...string) *batch {
	return &batch{db: db, table: table, cols: cols}
}

func (b *batch) add(vals ...any) {
	b.rows = append(b.rows, vals)
	if len(b.rows) >= 500 {
		b.flush()
	}
}

func (b *batch) flush() {
	if len(b.rows) == 0 {
		return
	}
	ph := "(" + strings.TrimRight(strings.Repeat("?,", len(b.cols)), ",") + ")"
	var sb strings.Builder
	sb.WriteString("INSERT INTO " + b.table + " (" + strings.Join(b.cols, ",") + ") VALUES ")
	args := make([]any, 0, len(b.rows)*len(b.cols))
	for i, r := range b.rows {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(ph)
		args = append(args, r...)
	}
	if _, err := b.db.Exec(sb.String(), args...); err != nil {
		log.Fatalf("insert %s: %v", b.table, err)
	}
	b.rows = b.rows[:0]
}

// emitters agrupa los batches de las tablas que se llenan.
type emitters struct {
	ev   *batch // chat_events
	fe   *batch // flow_events
	sess *batch // sessions
	wl   *batch // waiting_list
	np   *batch // notification_pending
	nh   *batch // notification_history
	cl   *batch // confirmation_log
	s    *summary
}

func newEmitters(db *sql.DB, s *summary) *emitters {
	return &emitters{
		ev:   newBatch(db, "chat_events", "session_id", "phone_number", "event_type", "event_data", "created_at"),
		fe:   newBatch(db, "flow_events", "trace_id", "flow", "step", "level", "outcome", "reason", "phone", "ref_type", "ref_id", "created_at"),
		sess: newBatch(db, "sessions", "id", "phone_number", "current_state", "status", "patient_id", "patient_doc", "patient_name", "patient_age", "patient_gender", "patient_entity", "conversation_id", "last_activity_at", "escalated_at", "expires_at", "created_at", "updated_at", "agent_id", "agent_name", "last_agent_msg_at", "resumed_at"),
		wl:   newBatch(db, "waiting_list", "id", "phone_number", "patient_id", "patient_doc", "patient_name", "patient_age", "patient_gender", "patient_entity", "patient_contract", "cups_code", "cups_name", "is_contrasted", "is_sedated", "espacios", "procedures_json", "procedure_type", "status", "created_at", "updated_at", "expires_at"),
		np:   newBatch(db, "notification_pending", "phone", "type", "appointment_id", "retry_count", "expires_at", "created_at"),
		nh:   newBatch(db, "notification_history", "phone", "type", "appointment_id", "status", "created_at", "resolved_at"),
		cl:   newBatch(db, "confirmation_log", "appointment_id", "patient_id", "confirmed_at", "channel", "channel_id", "created_at"),
		s:    s,
	}
}

func (e *emitters) flushAll() {
	for _, b := range []*batch{e.ev, e.fe, e.sess, e.wl, e.np, e.nh, e.cl} {
		b.flush()
	}
}

func ts(t time.Time) string { return t.Format("2006-01-02 15:04:05") }

// event emite un chat_event y lo cuenta en el summary.
func (e *emitters) event(sid, phone, etype string, data map[string]any, t time.Time) {
	var jd any
	if data != nil {
		j, _ := json.Marshal(data)
		jd = string(j)
	}
	e.ev.add(nz(sid), phone, etype, jd, ts(t))
	e.s.inc("ev:" + etype)
}

func (e *emitters) flow(trace, flow, step string, level int, outcome, reason, phone, refType, refID string, t time.Time) {
	e.fe.add(trace, flow, step, level, outcome, nz(reason), phone, nz(refType), nz(refID), ts(t))
	e.s.inc("flow:" + flow + "/" + step)
}

func nz(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ---------------------------------------------------------------------------
// Fase 1 — backbone derivado de citas SIESA
// ---------------------------------------------------------------------------

func seedFromCitas(ldb, sdb *sql.DB, catalog []cup, days int, s *summary) {
	cupName := map[string]string{}
	for _, c := range catalog {
		cupName[c.code] = c.name
	}

	q := `
SELECT c.id, c.cod_medi, c.asunto, CONVERT(VARCHAR(10),c.fecha,23) AS fecha,
  RTRIM(ISNULL(c.hora,'')) AS hora, RTRIM(ISNULL(c.meridiano,'')) AS merid,
  RTRIM(ISNULL(c.estado,'')) AS estado, CAST(ISNULL(c.AsistenciaConfirmada,0) AS INT) AS asist,
  CASE WHEN c.estado='C' THEN 1 ELSE 0 END AS cancelada,
  CONVERT(VARCHAR(19), ISNULL(c.fecha_solicitud, c.fecha), 120) AS solicitud,
  RTRIM(ISNULL(p.NombreCompleto,'')) AS nom,
  ISNULL(NULLIF(RTRIM(p.celular),''), RTRIM(ISNULL(p.telefono,''))) AS tel,
  RTRIM(ISNULL(p.sexo,'')) AS sexo, CONVERT(VARCHAR(10), p.fecha_naci, 23) AS nac,
  RTRIM(ISNULL(p.NUMEROCY,'')) AS doc,
  ISNULL((SELECT TOP 1 RTRIM(cp.id_procedimiento) FROM citas_procedimientos cp WITH(NOLOCK) WHERE cp.id_cita=c.id),
         (SELECT TOP 1 RTRIM(cpa.CodProcedimiento) FROM citas_procedimientos_asuntos cpa WITH(NOLOCK) WHERE cpa.IdCita=c.id)) AS cups
FROM citas c WITH(NOLOCK)
JOIN sis_paci p WITH(NOLOCK) ON p.autoid = c.autoid
WHERE c.fecha_solicitud >= DATEADD(DAY, @p1, GETDATE())
  AND c.fecha_solicitud <  DATEADD(DAY, 1, GETDATE())
ORDER BY c.id`
	rows, err := sdb.Query(q, -days)
	must(err, "query citas")
	defer rows.Close()

	e := newEmitters(ldb, s)
	n := 0
	for rows.Next() {
		var id, codMedi, asunto, asist, cancelada int
		var fecha, hora, merid, estado, solicitud, nom, tel, sexo, nac, doc, cups sql.NullString
		must(rows.Scan(&id, &codMedi, &asunto, &fecha, &hora, &merid, &estado, &asist, &cancelada,
			&solicitud, &nom, &tel, &sexo, &nac, &doc, &cups), "scan cita")

		apptID := fmt.Sprintf("%d", id)
		docVal := doc.String
		if docVal == "" {
			docVal = "AP" + apptID
		}
		phone := normalizePhone(tel.String)
		name := cleanName(nom.String)
		gender := gender(sexo.String)
		age := ageFrom(nac.String)
		entity := entities[id%len(entities)]
		svc := asuntoService[asunto]
		cupsCode := cups.String
		cupsName := cupName[cupsCode]
		if cupsName == "" {
			cupsName = svc
		}
		base := randTSUniform(days)
		confirmed := asist == 1 || estado.String == "CC" || estado.String == "A"

		sid := uuid.NewString()
		status := "completed"
		// sesión
		e.sess.add(sid, phone, "MAIN_MENU", status, nz(doc.String), nz(doc.String), nz(name),
			nullInt(age), nz(gender), nz(entity), nz("conv-"+apptID),
			ts(base.Add(8*time.Minute)), nil, ts(base.Add(72*time.Hour)), ts(base), ts(base.Add(8*time.Minute)),
			nil, nil, nil, nil)

		// secuencia coherente de chat_events
		seq := []struct {
			et  string
			d   map[string]any
			off time.Duration
		}{
			{"session_started", nil, 0},
			{"patient_found", map[string]any{"doc": doc.String, "name": name}, 30 * time.Second},
			{"document_entered", map[string]any{"doc_length": 10}, 1 * time.Minute},
			{"menu_selected", map[string]any{"option": "agendar"}, 90 * time.Second},
			{"order_method_selected", nil, 2 * time.Minute},
			{"ocr_success", nil, 3 * time.Minute},
			{"ocr_validated", nil, 3*time.Minute + 20*time.Second},
			{"validations_complete", nil, 4 * time.Minute},
			{"client_type_selected", map[string]any{"type": entity, "category": "EPS"}, 4*time.Minute + 30*time.Second},
			{"entity_checked", map[string]any{"entity": entity, "active": true}, 5 * time.Minute},
			{"slots_found", map[string]any{"count": 3 + id%5}, 6 * time.Minute},
			{"slot_selected", map[string]any{"id": 1, "time_slot": hora.String}, 6*time.Minute + 30*time.Second},
			{"booking_confirmed", map[string]any{"appointment_id": apptID}, 7 * time.Minute},
			{"appointment_created", map[string]any{"service_type": svc, "cups_code": cupsCode, "cups_name": cupsName, "appointment_id": apptID, "date": fecha.String, "time_slot": hora.String}, 7*time.Minute + 10*time.Second},
		}
		for _, x := range seq {
			e.event(sid, phone, x.et, x.d, base.Add(x.off))
		}
		e.event(sid, phone, "session_completed", nil, base.Add(8*time.Minute))
		e.event(sid, phone, "message_sent", map[string]any{"message_type": "interactive_list"}, base.Add(10*time.Second))

		trace := "sess:" + sid
		e.flow(trace, "agendar", "ocr_ok", 3, "ok", "", phone, "", "", base.Add(3*time.Minute))
		e.flow(trace, "agendar", "slots_found", 3, "ok", "", phone, "", "", base.Add(6*time.Minute))
		e.flow(trace, "agendar", "booking_success", 2, "ok", "", phone, "cita", apptID, base.Add(7*time.Minute))

		// confirmación / cancelación según el estado real
		notifBase := base.Add(24 * time.Hour)
		if notifBase.After(now) {
			notifBase = now.Add(-2 * time.Hour)
		}
		e.event("", phone, "notification_sent", map[string]any{"type": "confirmation", "appointment_id": apptID}, notifBase)
		e.flow("notif:"+apptID, "notif_recordatorio", "reminder_sent", 3, "ok", "", phone, "bird_msg", "msg-"+apptID, notifBase)
		ivr := id%4 == 0
		if confirmed {
			ch := notifBase.Add(20 * time.Minute)
			if ivr {
				e.event("", phone, "notification_ivr_sent", nil, notifBase)
				e.event("", phone, "notification_confirmed_ivr", map[string]any{"appointment_id": apptID}, ch)
				e.cl.add(apptID, docVal, ts(ch), "ivr", "call-"+apptID, ts(ch))
			} else {
				e.event("", phone, "notification_confirmed", map[string]any{"appointment_id": apptID, "response_time_min": 20}, ch)
				e.cl.add(apptID, docVal, ts(ch), "whatsapp", "msg-"+apptID, ts(ch))
			}
			e.event("", phone, "appointment_confirmed", map[string]any{"appointment_id": apptID}, ch)
			e.nh.add(phone, "confirmation", apptID, "confirmed", ts(notifBase), ts(ch))
			e.flow("notif:"+apptID, "notif_recordatorio", "confirmed", 2, "ok", "", phone, "cita", apptID, ch)
			s.inc("confirmadas")
		} else if cancelada == 1 {
			ch := notifBase.Add(35 * time.Minute)
			e.event("", phone, "notification_cancel_confirmed", map[string]any{"appointment_id": apptID}, ch)
			e.event("", phone, "appointment_cancelled", map[string]any{"appointment_id": apptID}, ch)
			e.nh.add(phone, "confirmation", apptID, "cancelled", ts(notifBase), ts(ch))
			e.flow("notif:"+apptID, "notif_recordatorio", "cancelled", 2, "ok", "", phone, "cita", apptID, ch)
			s.inc("canceladas")
		} else {
			// sin respuesta
			e.event("", phone, "notification_timeout", map[string]any{"appointment_id": apptID}, notifBase.Add(6*time.Hour))
			e.nh.add(phone, "confirmation", apptID, "no_response", ts(notifBase), ts(notifBase.Add(6*time.Hour)))
		}

		s.inc("citas")
		n++
	}
	must(rows.Err(), "rows citas")
	e.flushAll()
	fmt.Printf("fase 1: %d citas → backbone derivado\n", n)
	s.citas = n
}

// ---------------------------------------------------------------------------
// Fase 2 — flujos sintéticos sin cita
// ---------------------------------------------------------------------------

func seedSynthetic(ldb *sql.DB, catalog []cup, days int, leakRatio float64, s *summary) {
	e := newEmitters(ldb, s)
	nLeak := int(float64(s.citas) * leakRatio)
	if nLeak < 80 {
		nLeak = 80
	}

	leaks := []struct {
		ev, flowStep string
	}{
		{"cups_none", "cups_none"},
		{"no_slots_found", "no_slots"},
		{"already_has_appt", "already_has_appt"},
		{"booking_failed", "booking_failed"},
		{"ocr_failed", "ocr_failed"},
		{"slot_taken", "no_slots"},
	}
	for i := 0; i < nLeak; i++ {
		t := randTS(days)
		sid := uuid.NewString()
		phone := fakePhone(i)
		ent := entities[rand.Intn(len(entities))]
		e.sess.add(sid, phone, "MAIN_MENU", "abandoned", nil, nil, nil, nil, nil, nz(ent), nil,
			ts(t.Add(4*time.Minute)), nil, ts(t.Add(72*time.Hour)), ts(t), ts(t.Add(4*time.Minute)),
			nil, nil, nil, nil)
		e.event(sid, phone, "session_started", nil, t)
		e.event(sid, phone, "menu_selected", map[string]any{"option": "agendar"}, t.Add(time.Minute))
		lk := leaks[i%len(leaks)]
		switch lk.ev {
		case "ocr_failed":
			e.event(sid, phone, "ocr_failed", nil, t.Add(2*time.Minute))
			e.flow("sess:"+sid, "agendar", "ocr_failed", 1, "error", "ocr_parse", phone, "", "", t.Add(2*time.Minute))
		default:
			e.event(sid, phone, "ocr_success", nil, t.Add(2*time.Minute))
			e.flow("sess:"+sid, "agendar", "ocr_ok", 3, "ok", "", phone, "", "", t.Add(2*time.Minute))
			e.event(sid, phone, lk.ev, nil, t.Add(4*time.Minute))
			oc := "blocked"
			if lk.ev == "booking_failed" {
				oc = "error"
			}
			e.flow("sess:"+sid, "agendar", lk.flowStep, 2, oc, lk.ev, phone, "", "", t.Add(4*time.Minute))
		}
		e.event(sid, phone, "session_closed_inactivity", nil, t.Add(20*time.Minute))
	}

	// registros
	for i := 0; i < nLeak/3+20; i++ {
		t := randTS(days)
		sid := uuid.NewString()
		phone := fakePhone(10000 + i)
		e.event(sid, phone, "registration_started", nil, t)
		e.flow("sess:"+sid, "registro", "registration_started", 3, "ok", "", phone, "", "", t)
		if i%5 == 0 {
			e.event(sid, phone, "registration_failed", nil, t.Add(3*time.Minute))
			e.flow("sess:"+sid, "registro", "patient_create_failed", 1, "error", "siesa", phone, "", "", t.Add(3*time.Minute))
		} else {
			e.event(sid, phone, "registration_success", nil, t.Add(3*time.Minute))
			e.flow("sess:"+sid, "registro", "patient_created", 2, "ok", "", phone, "", "", t.Add(3*time.Minute))
		}
	}

	// escalaciones (con agente real de Bird + 3 escenarios: sin atender / atendió sin devolver / ideal)
	agents := loadBirdAgents()
	for i := 0; i < nLeak/2+30; i++ {
		t := randTS(days)
		sid := uuid.NewString()
		phone := fakePhone(20000 + i)
		from := escalationStates[rand.Intn(len(escalationStates))]
		ag := agents[i%len(agents)]
		escAt := t.Add(2 * time.Minute)

		// Reparto: ~20% sin atender, ~30% atendió sin devolver, ~50% ideal (atendió y devolvió).
		var lastAgent, resumed any
		status := "abandoned"
		switch sc := i % 10; {
		case sc < 2: // sin atender: el agente nunca contestó
		case sc < 5: // atendió sin devolver: contestó al paciente pero no usó /bot
			lastAgent = ts(escAt.Add(time.Duration(1+rand.Intn(40)) * time.Minute))
		default: // ideal: atendió y devolvió al bot
			la := escAt.Add(time.Duration(1+rand.Intn(40)) * time.Minute)
			lastAgent = ts(la)
			resumed = ts(la.Add(time.Duration(2+rand.Intn(30)) * time.Minute))
			status = "completed"
		}

		e.sess.add(sid, phone, from, status, nil, nil, nil, nil, nil, nil, nil,
			ts(t), ts(escAt), ts(t.Add(72*time.Hour)), ts(t), ts(escAt),
			ag.id, ag.name, lastAgent, resumed)
		e.event(sid, phone, "escalated_to_agent", map[string]any{"from_state": from, "agent": ag.name}, escAt)
		e.flow("sess:"+sid, "escalacion", "escalated", 2, "escalated", "", phone, "", "", escAt)
		switch i % 4 {
		case 0:
			e.event(sid, phone, "agent_reminder_sent", nil, t.Add(20*time.Minute))
			e.flow("sess:"+sid, "escalacion", "agent_reminder_sent", 3, "retry", "", phone, "", "", t.Add(20*time.Minute))
		case 1:
			e.event(sid, phone, "escalation_expired", nil, t.Add(2*time.Hour))
			e.flow("sess:"+sid, "escalacion", "escalation_expired", 2, "info", "", phone, "", "", t.Add(2*time.Hour))
		case 2:
			e.event(sid, phone, "escalation_failed", nil, t.Add(3*time.Minute))
		}
	}

	// lista de espera (todos los status) + eventos
	wlStatus := []string{"waiting", "notified", "scheduled", "declined", "expired"}
	for i := 0; i < nLeak/2+40; i++ {
		t := randTS(days)
		st := wlStatus[i%len(wlStatus)]
		c := catalog[rand.Intn(len(catalog))]
		phone := fakePhone(30000 + i)
		wlID := uuid.NewString()
		e.wl.add(wlID, phone, fmt.Sprintf("P%05d", i), fmt.Sprintf("%d", 10000000+rand.Intn(80000000)),
			"PACIENTE ESPERA "+fmt.Sprintf("%d", i), 20+rand.Intn(60), pick("F", "M"), entities[rand.Intn(len(entities))], "8",
			c.code, c.name, 0, 0, 1, "[]", svcType(c.asunto), st, ts(t), ts(t), ts(t.Add(360*time.Hour)))
		e.event(newSID(), phone, "waiting_list_joined", map[string]any{"cups_code": c.code}, t)
		e.flow("wl:"+wlID, "lista_espera", "enrolled", 3, "ok", "", phone, "", "", t)
		switch st {
		case "scheduled":
			e.event("", phone, "waiting_list_booking_success", map[string]any{"cups_code": c.code}, t.Add(48*time.Hour))
			e.event("", phone, "waiting_list_schedule_accepted", nil, t.Add(48*time.Hour))
			e.flow("wl:"+wlID, "lista_espera", "booked", 2, "ok", "", phone, "cita", "", t.Add(48*time.Hour))
		case "declined":
			e.event("", phone, "waiting_list_schedule_declined", nil, t.Add(24*time.Hour))
			e.flow("wl:"+wlID, "lista_espera", "declined", 2, "info", "", phone, "", "", t.Add(24*time.Hour))
		case "notified":
			e.flow("wl:"+wlID, "lista_espera", "notified", 3, "ok", "", phone, "bird_msg", "", t.Add(12*time.Hour))
		}
	}

	// notificaciones extra (reagendas, admin, IVR cancel) + pendientes en vivo
	for i := 0; i < nLeak/3+30; i++ {
		t := randTS(days)
		phone := fakePhone(40000 + i)
		apptID := fmt.Sprintf("9%06d", i)
		switch i % 6 {
		case 0:
			e.event("", phone, "notification_reschedule_confirmed", map[string]any{"appointment_id": apptID}, t)
		case 1:
			e.event("", phone, "notification_reschedule_self_service", map[string]any{"appointment_id": apptID}, t)
		case 2:
			e.event("", phone, "notification_cancelled_ivr", map[string]any{"appointment_id": apptID}, t)
			e.event("", phone, "notification_ivr_sent", nil, t.Add(-time.Hour))
		case 3:
			e.event("", phone, "notification_escalated_agent", map[string]any{"appointment_id": apptID}, t)
		case 4:
			e.event("", phone, "admin_cancel_agenda", map[string]any{"agenda_id": 100 + i, "reason": "médico incapacitado"}, t)
			e.flow(fmt.Sprintf("agenda:%d:%s", 100+i, t.Format("20060102")), "admin_agenda", "agenda_cancelled", 2, "ok", "", phone, "agenda", "", t)
		case 5:
			e.event("", phone, "admin_reschedule_agenda", map[string]any{"agenda_id": 200 + i, "slots_offered": 8}, t)
			e.flow(fmt.Sprintf("agenda:%d:%s", 200+i, t.Format("20060102")), "admin_agenda", "agenda_rescheduled", 2, "ok", "", phone, "agenda", "", t)
		}
	}

	// otros bloqueos / fuera de horario / OCR clínico
	for i := 0; i < nLeak/3+20; i++ {
		t := randTS(days)
		phone := fakePhone(50000 + i)
		switch i % 5 {
		case 0:
			e.event(newSID(), phone, "out_of_hours", map[string]any{"day": "sábado", "hour": 20}, nightTS(days))
		case 1:
			e.event(newSID(), phone, "gfr_calculated", map[string]any{"creatinine": 1.2, "gfr_value": 55}, t)
		case 2:
			e.event(newSID(), phone, "pregnant_blocked", nil, t)
		case 3:
			e.event(newSID(), phone, "gfr_not_eligible", nil, t)
		case 4:
			e.event(newSID(), phone, "max_retries_reached", map[string]any{"state": "ASK_ENTITY_NUMBER", "retries": 3}, t)
		}
		if i%7 == 0 {
			e.event(newSID(), phone, "coverage_no_convenio", map[string]any{"cup": "930860"}, t)
		}
	}

	// notification_pending EN VIVO (no expiradas) para el widget + sesiones activas
	for i := 0; i < 12; i++ {
		phone := fakePhone(60000 + i)
		apptID := fmt.Sprintf("7%06d", i)
		created := now.Add(-time.Duration(10+i*5) * time.Minute)
		e.np.add(phone, "confirmation", apptID, i%3, ts(now.Add(time.Duration(2+i)*time.Hour)), ts(created))
		e.event("", phone, "notification_sent", map[string]any{"type": "confirmation", "appointment_id": apptID}, created)
	}
	for i := 0; i < 6; i++ {
		phone := fakePhone(61000 + i)
		sidv := uuid.NewString()
		e.sess.add(sidv, phone, "SHOW_SLOTS", "active", nil, nil, nil, nil, nil, nz(entities[i%len(entities)]), nil,
			ts(now.Add(-time.Duration(i+1)*time.Minute)), nil, ts(now.Add(2*time.Hour)), ts(now.Add(-30*time.Minute)), ts(now.Add(-time.Duration(i+1)*time.Minute)),
			nil, nil, nil, nil)
		e.s.inc("sesiones_activas")
	}

	e.flushAll()
	fmt.Printf("fase 2: %d fugas + flujos sintéticos\n", nLeak)
}

// ---------------------------------------------------------------------------
// helpers de datos
// ---------------------------------------------------------------------------

func newSID() string { return uuid.NewString() }

// seedAgent es un agente para asignar a las escalaciones sembradas.
type seedAgent struct{ id, name string }

// loadBirdAgents intenta traer los agentes REALES de Bird (mismos que el bot usa al escalar). Si Bird
// no está configurado o la API falla, cae a una lista sintética para que el seeder funcione offline.
func loadBirdAgents() []seedAgent {
	cfg := config.Load()
	if cfg.BirdAccessKeyID == "" || cfg.BirdWorkspaceID == "" {
		fmt.Println("Bird no configurado (BIRD_ACCESS_KEY_ID/BIRD_WORKSPACE_ID) → agentes sintéticos")
		return fallbackAgents()
	}
	infos, err := bird.NewClient(cfg).ListAllAgents()
	if err != nil || len(infos) == 0 {
		fmt.Printf("Bird ListAllAgents sin datos (%v) → agentes sintéticos\n", err)
		return fallbackAgents()
	}
	out := make([]seedAgent, 0, len(infos))
	for _, a := range infos {
		name := strings.TrimSpace(a.DisplayName)
		if name == "" {
			name = a.ID
		}
		out = append(out, seedAgent{a.ID, name})
	}
	fmt.Printf("Bird: %d agentes reales cargados para el SLA por agente\n", len(out))
	return out
}

func fallbackAgents() []seedAgent {
	return []seedAgent{
		{"agt-001", "Laura Gómez"},
		{"agt-002", "Carlos Ruiz"},
		{"agt-003", "Ana Torres"},
		{"agt-004", "Diego Marín"},
		{"agt-005", "Paula Castro"},
		{"agt-006", "Jorge Niño"},
	}
}

func randTS(days int) time.Time {
	var day time.Time
	if rand.Float64() < 0.20 {
		day = now
	} else {
		day = now.AddDate(0, 0, -rand.Intn(days))
	}
	return time.Date(day.Year(), day.Month(), day.Day(), 7+rand.Intn(11), rand.Intn(60), rand.Intn(60), 0, time.Local)
}

func nightTS(days int) time.Time {
	day := now.AddDate(0, 0, -rand.Intn(days))
	return time.Date(day.Year(), day.Month(), day.Day(), 19+rand.Intn(4), rand.Intn(60), 0, 0, time.Local)
}

// randTSUniform reparte uniforme sobre [hoy-días, hoy] (incluye hoy) en horario hábil. Se usa para
// el backbone de citas: da un volumen por día realista (≈ citas/ventana) con cobertura de hoy, sin
// el pico extra de randTS.
func randTSUniform(days int) time.Time {
	day := now.AddDate(0, 0, -rand.Intn(days))
	return time.Date(day.Year(), day.Month(), day.Day(), 7+rand.Intn(11), rand.Intn(60), rand.Intn(60), 0, time.Local)
}

func normalizePhone(t string) string {
	d := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, t)
	if len(d) >= 10 {
		return "+57" + d[len(d)-10:]
	}
	return fakePhone(len(d) + rand.Intn(1000))
}

func fakePhone(seed int) string { return fmt.Sprintf("+5730%07d", 1000000+seed%9000000) }

func cleanName(n string) string {
	if i := strings.Index(n, " : "); i >= 0 {
		return strings.TrimSpace(n[i+3:])
	}
	return strings.TrimSpace(n)
}

func gender(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "F" || s == "M" {
		return s
	}
	return ""
}

func ageFrom(nac string) int {
	t, err := time.Parse("2006-01-02", nac)
	if err != nil {
		return 30 + rand.Intn(50)
	}
	a := now.Year() - t.Year()
	if now.YearDay() < t.YearDay() {
		a--
	}
	if a < 0 || a > 110 {
		return 40
	}
	return a
}

func svcType(asunto int) string {
	if s, ok := asuntoService[asunto]; ok {
		return s
	}
	return ""
}

func pick(opts ...string) string { return opts[rand.Intn(len(opts))] }

func nullInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

// ---------------------------------------------------------------------------
// summary
// ---------------------------------------------------------------------------

type summary struct {
	citas  int
	counts map[string]int
}

func (s *summary) inc(k string) { s.counts[k]++ }

func (s *summary) print() {
	fmt.Println("\n== resumen esperado (cruzar con el dashboard) ==")
	keys := make([]string, 0, len(s.counts))
	for k := range s.counts {
		keys = append(keys, k)
	}
	// orden simple alfabético
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	for _, k := range keys {
		if strings.HasPrefix(k, "ev:") || k == "citas" || k == "confirmadas" || k == "canceladas" || k == "sesiones_activas" {
			fmt.Printf("  %-40s %d\n", k, s.counts[k])
		}
	}
	fmt.Printf("  %-40s %d\n", "citas (backbone)", s.citas)
}
