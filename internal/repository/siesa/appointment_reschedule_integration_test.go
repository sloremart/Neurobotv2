//go:build integration

// Integration test for RescheduleDayOfAgenda against a real SIESA database.
// Run with: go test -tags integration -run TestRescheduleDayOfAgenda -v ./internal/repository/siesa/
// Requires SIESA_DSN, e.g.
//
//	sqlserver://sa:pass@localhost:1433?database=ZeusSalud_Neuro&encrypt=disable
//
// The test snapshots the source day (citas + slots), runs the REAL method (which commits), asserts the
// outcome against the DB, and then fully RESTORES the original state (and deletes any agenda it created),
// so it is safe to run against the shared local DB.
package siesa

import (
	"context"
	"database/sql"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/domain"
)

func TestRescheduleDayOfAgenda(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	repo := NewAppointmentRepo(db, "", 0)
	ctx := context.Background()

	const srcAgenda = 705
	const oldDate = "2026-07-09"

	type citaSnap struct {
		id     string
		estado string
		idProg int
		fecha  string
	}
	type slotSnap struct {
		id     int
		idCita sql.NullInt64
		bloq   bool
	}

	snapCitas := func(t *testing.T) []citaSnap {
		t.Helper()
		var cs []citaSnap
		rows, err := db.QueryContext(ctx, `
			SELECT CAST(id AS VARCHAR(20)), estado, id_programacion, CONVERT(VARCHAR(10),fecha,23)
			FROM citas WHERE id_programacion=@p1 AND CAST(fecha AS DATE)=@p2 AND estado<>'C'`,
			srcAgenda, oldDate)
		if err != nil {
			t.Fatalf("snapshot citas: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var c citaSnap
			if err := rows.Scan(&c.id, &c.estado, &c.idProg, &c.fecha); err != nil {
				t.Fatalf("scan cita: %v", err)
			}
			cs = append(cs, c)
		}
		return cs
	}

	snapSlots := func(t *testing.T, agenda int, date string) []slotSnap {
		t.Helper()
		var ss []slotSnap
		rows, err := db.QueryContext(ctx, `
			SELECT Id, IdCita, Bloqueado FROM programacion_medico_detalle
			WHERE IdProgramacionMedico=@p1 AND CAST(Fecha AS DATE)=@p2`, agenda, date)
		if err != nil {
			t.Fatalf("snapshot slots: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var s slotSnap
			if err := rows.Scan(&s.id, &s.idCita, &s.bloq); err != nil {
				t.Fatalf("scan slot: %v", err)
			}
			ss = append(ss, s)
		}
		return ss
	}

	// restore devuelve las citas y TODOS los slots capturados (origen + destino) a su estado exacto y
	// borra la agenda creada (si la hubo).
	restore := func(cs []citaSnap, allSlots []slotSnap, createdAgenda int) {
		// 1) Liberar cualquier slot (origen o destino) que apunte a estas citas.
		for _, c := range cs {
			_, _ = db.ExecContext(ctx, `UPDATE programacion_medico_detalle SET IdCita=NULL WHERE IdCita=@p1`, c.id)
		}
		// 2) Devolver las citas a la agenda/fecha/estado originales.
		for _, c := range cs {
			_, _ = db.ExecContext(ctx,
				`UPDATE citas SET id_programacion=@p1, fecha=@p2, estado=@p3 WHERE id=@p4`,
				c.idProg, c.fecha, c.estado, c.id)
		}
		// 3) Restaurar los slots capturados (IdCita + Bloqueado exactos): origen y destino.
		for _, s := range allSlots {
			_, _ = db.ExecContext(ctx,
				`UPDATE programacion_medico_detalle SET IdCita=@p1, Bloqueado=@p2 WHERE Id=@p3`,
				s.idCita, s.bloq, s.id)
		}
		// 4) Borrar la agenda creada por el escenario CREATE.
		if createdAgenda > 0 {
			_, _ = db.ExecContext(ctx, `DELETE FROM programacion_medico_detalle WHERE IdProgramacionMedico=@p1`, createdAgenda)
			_, _ = db.ExecContext(ctx, `DELETE FROM programacion_medico_relacion_asunto
				WHERE IdProgramacionMedicoRelacion IN (SELECT Id FROM programacion_medico_relacion WHERE id_programacion=@p1)`, createdAgenda)
			_, _ = db.ExecContext(ctx, `DELETE FROM programacion_medico_relacion WHERE id_programacion=@p1`, createdAgenda)
			_, _ = db.ExecContext(ctx, `DELETE FROM programacion_medico WHERE id=@p1`, createdAgenda)
		}
	}

	countOccupied := func(t *testing.T, agenda int, date string) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM programacion_medico_detalle WHERE IdProgramacionMedico=@p1 AND CAST(Fecha AS DATE)=@p2 AND IdCita IS NOT NULL`,
			agenda, date).Scan(&n); err != nil {
			t.Fatalf("count occupied: %v", err)
		}
		return n
	}
	sourceBlocked := func(t *testing.T) (blocked, stillOccupied int) {
		t.Helper()
		_ = db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM programacion_medico_detalle WHERE IdProgramacionMedico=@p1 AND CAST(Fecha AS DATE)=@p2 AND Bloqueado=1`,
			srcAgenda, oldDate).Scan(&blocked)
		_ = db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM programacion_medico_detalle WHERE IdProgramacionMedico=@p1 AND CAST(Fecha AS DATE)=@p2 AND IdCita IS NOT NULL`,
			srcAgenda, oldDate).Scan(&stillOccupied)
		return
	}

	// Escenario CREATE: duplicar la agenda para un día libre del médico y mover el día ahí.
	t.Run("CREATE_duplicando", func(t *testing.T) {
		const newDate = "2026-12-19" // médico 8 sin agenda ese día (verificado)
		cs := snapCitas(t)
		srcSlots := snapSlots(t, srcAgenda, oldDate)
		want := len(cs)
		var created int
		defer func() { restore(cs, srcSlots, created) }()

		res, err := repo.RescheduleDayOfAgenda(ctx, domain.RescheduleDayInput{
			AgendaID: srcAgenda, OldDate: oldDate, NewDate: newDate, DestAgendaID: 0,
		})
		if err != nil {
			t.Fatalf("RescheduleDayOfAgenda CREATE: %v", err)
		}
		created = res.DestAgendaID

		if !res.Created {
			t.Errorf("esperaba Created=true (nueva agenda duplicada)")
		}
		if res.DestAgendaID <= 0 {
			t.Fatalf("esperaba DestAgendaID>0, got %d", res.DestAgendaID)
		}
		if res.Moved != want {
			t.Errorf("Moved=%d, esperaba %d", res.Moved, want)
		}
		if occ := countOccupied(t, res.DestAgendaID, newDate); occ != want {
			t.Errorf("slots ocupados en destino=%d, esperaba %d", occ, want)
		}
		if blocked, stillOcc := sourceBlocked(t); stillOcc != 0 || blocked < want {
			t.Errorf("día origen no cerrado: bloqueados=%d ocupados=%d (esperaba ocupados=0, bloqueados>=%d)", blocked, stillOcc, want)
		}
		t.Logf("OK CREATE: agenda %d creada, %d citas movidas, día origen bloqueado", res.DestAgendaID, res.Moved)
	})

	// Escenario MOVE: mover el día a una agenda existente del mismo médico con misma grilla libre.
	t.Run("MOVE_a_existente", func(t *testing.T) {
		const destAgenda = 711
		const newDate = "2026-07-16" // 711 es la reserva del médico (misma grilla, Bloqueado=1) (verificado)
		cs := snapCitas(t)
		// Capturar origen Y destino: el MOVE activa (Bloqueado=0) los cupos reserva de 711 → restaurarlos.
		allSlots := append(snapSlots(t, srcAgenda, oldDate), snapSlots(t, destAgenda, newDate)...)
		want := len(cs)
		defer func() { restore(cs, allSlots, 0) }()

		res, err := repo.RescheduleDayOfAgenda(ctx, domain.RescheduleDayInput{
			AgendaID: srcAgenda, OldDate: oldDate, NewDate: newDate, DestAgendaID: destAgenda,
		})
		if err != nil {
			t.Fatalf("RescheduleDayOfAgenda MOVE: %v", err)
		}
		if res.Created {
			t.Errorf("esperaba Created=false (agenda existente)")
		}
		if res.DestAgendaID != destAgenda {
			t.Errorf("DestAgendaID=%d, esperaba %d", res.DestAgendaID, destAgenda)
		}
		if res.Moved != want {
			t.Errorf("Moved=%d, esperaba %d", res.Moved, want)
		}
		if occ := countOccupied(t, destAgenda, newDate); occ != want {
			t.Errorf("slots ocupados en 711=%d, esperaba %d", occ, want)
		}
		if blocked, stillOcc := sourceBlocked(t); stillOcc != 0 || blocked < want {
			t.Errorf("día origen no cerrado: bloqueados=%d ocupados=%d", blocked, stillOcc)
		}
		t.Logf("OK MOVE: %d citas movidas a agenda %d, día origen bloqueado", res.Moved, destAgenda)
	})

	// DryRun: valida + calcula el resumen pero NO muta (rollback). Verifica que la BD quede intacta.
	t.Run("DRY_RUN_no_muta", func(t *testing.T) {
		const destAgenda = 711
		const newDate = "2026-07-16"
		res, err := repo.RescheduleDayOfAgenda(ctx, domain.RescheduleDayInput{
			AgendaID: srcAgenda, OldDate: oldDate, NewDate: newDate, DestAgendaID: destAgenda, DryRun: true,
		})
		if err != nil {
			t.Fatalf("dry_run: %v", err)
		}
		if res.Moved != 12 || res.DestAgendaID != destAgenda || res.Created {
			t.Errorf("resumen dry_run inesperado: %+v", res)
		}
		// BD intacta: origen sigue ocupado, destino sigue vacío, origen sin bloquear.
		if occ := countOccupied(t, srcAgenda, oldDate); occ != 12 {
			t.Errorf("dry_run MUTÓ el origen: ocupados=%d (esperaba 12)", occ)
		}
		if occ := countOccupied(t, destAgenda, newDate); occ != 0 {
			t.Errorf("dry_run MUTÓ el destino: ocupados=%d (esperaba 0)", occ)
		}
		if blocked, _ := sourceBlocked(t); blocked != 0 {
			t.Errorf("dry_run bloqueó el origen: bloqueados=%d (esperaba 0)", blocked)
		}
		t.Logf("OK DRY_RUN: resumen moved=%d sin mutar la BD", res.Moved)
	})

	// FindDoctorAgendasOnDate debe listar las agendas del médico ese día, incluida la reserva vacía 711.
	t.Run("doctor_agendas_on_date", func(t *testing.T) {
		ags, err := repo.FindDoctorAgendasOnDate(ctx, "8", "2026-07-16")
		if err != nil {
			t.Fatalf("FindDoctorAgendasOnDate: %v", err)
		}
		var r711 *domain.DoctorAgendaOnDate
		for i := range ags {
			if ags[i].AgendaID == 711 {
				r711 = &ags[i]
			}
		}
		if r711 == nil {
			t.Fatalf("no se listó la reserva 711; agendas=%+v", ags)
		}
		if r711.Slots == 0 || r711.Free != r711.Slots {
			t.Errorf("711 debería estar vacía (free==slots): %+v", *r711)
		}
		t.Logf("OK agendas destino del médico 8 el 2026-07-16: %+v", ags)
	})
}
