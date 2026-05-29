package datosipsndx

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/repository"
)

var _ repository.ScheduleRepository = (*ScheduleRepo)(nil)

type ScheduleRepo struct {
	db *sql.DB
}

func NewScheduleRepo(db *sql.DB) *ScheduleRepo {
	return &ScheduleRepo{db: db}
}

// parseDBTime converts '1899-12-30 07:00:00' or 'HH:mm' format to "HH:mm".
func parseDBTime(raw string) string {
	if raw == "" {
		return ""
	}
	// Format: "1899-12-30 07:00:00" → extract "07:00"
	if len(raw) >= 16 {
		return raw[11:16]
	}
	// Try as time string
	if t, err := time.Parse("15:04:05", raw); err == nil {
		return t.Format("15:04")
	}
	if len(raw) >= 5 {
		return raw[:5]
	}
	return raw
}

func (r *ScheduleRepo) FindFutureWorkingDays(ctx context.Context, doctorDocs []string) ([]domain.WorkingDay, error) {
	if len(doctorDocs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(doctorDocs))
	args := make([]interface{}, len(doctorDocs))
	for i, doc := range doctorDocs {
		placeholders[i] = "?"
		args[i] = doc
	}

	query := fmt.Sprintf(`SELECT IdTercero, Fecha, JornadaM, JornadaT, IdAgenda
	          FROM tblexepciondias
	          WHERE IdTercero IN (%s) AND Fecha >= CURDATE()
	            AND (JornadaM = -1 OR JornadaT = -1)
	          ORDER BY Fecha
	          LIMIT 90`, strings.Join(placeholders, ","))

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var days []domain.WorkingDay
	for rows.Next() {
		var d domain.WorkingDay
		var fecha time.Time
		var morningInt, afternoonInt int

		if err := rows.Scan(&d.DoctorDocument, &fecha, &morningInt, &afternoonInt, &d.AgendaID); err != nil {
			return nil, err
		}

		d.Date = fecha.Format("2006-01-02")
		d.MorningEnabled = (morningInt == -1)
		d.AfternoonEnabled = (afternoonInt == -1)
		days = append(days, d)
	}
	return days, rows.Err()
}

func (r *ScheduleRepo) FindScheduleConfig(ctx context.Context, scheduleID int, doctorDoc string) (*domain.ScheduleConfig, error) {
	query := `SELECT IdConfig, IdMedico, DuracionCita, Activo, IdAgenda, SesionesxCita,
	            Trabaja0, Trabaja1, Trabaja2, Trabaja3, Trabaja4, Trabaja5, Trabaja6,
	            HInicioM0, HFinalM0, HInicioT0, HFinalT0,
	            HInicioM1, HFinalM1, HInicioT1, HFinalT1,
	            HInicioM2, HFinalM2, HInicioT2, HFinalT2,
	            HInicioM3, HFinalM3, HInicioT3, HFinalT3,
	            HInicioM4, HFinalM4, HInicioT4, HFinalT4,
	            HInicioM5, HFinalM5, HInicioT5, HFinalT5,
	            HInicioM6, HFinalM6, HInicioT6, HFinalT6
	          FROM citas_conf
	          WHERE IdAgenda = ? AND IdMedico = ? AND Activo = -1
	          LIMIT 1`

	var cfg domain.ScheduleConfig
	var activeInt int
	var workDays [7]int
	var mStart, mEnd, aStart, aEnd [7]sql.NullString

	err := r.db.QueryRowContext(ctx, query, scheduleID, doctorDoc).Scan(
		&cfg.ID, &cfg.DoctorDocument, &cfg.AppointmentDuration, &activeInt, &cfg.AgendaID, &cfg.SessionsPerAppointment,
		&workDays[0], &workDays[1], &workDays[2], &workDays[3], &workDays[4], &workDays[5], &workDays[6],
		&mStart[0], &mEnd[0], &aStart[0], &aEnd[0],
		&mStart[1], &mEnd[1], &aStart[1], &aEnd[1],
		&mStart[2], &mEnd[2], &aStart[2], &aEnd[2],
		&mStart[3], &mEnd[3], &aStart[3], &aEnd[3],
		&mStart[4], &mEnd[4], &aStart[4], &aEnd[4],
		&mStart[5], &mEnd[5], &aStart[5], &aEnd[5],
		&mStart[6], &mEnd[6], &aStart[6], &aEnd[6],
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	cfg.IsActive = (activeInt != 0)
	for i := 0; i < 7; i++ {
		cfg.WorkDays[i] = (workDays[i] == -1)
		if mStart[i].Valid {
			cfg.MorningStart[i] = parseDBTime(mStart[i].String)
		}
		if mEnd[i].Valid {
			cfg.MorningEnd[i] = parseDBTime(mEnd[i].String)
		}
		if aStart[i].Valid {
			cfg.AfternoonStart[i] = parseDBTime(aStart[i].String)
		}
		if aEnd[i].Valid {
			cfg.AfternoonEnd[i] = parseDBTime(aEnd[i].String)
		}
	}

	return &cfg, nil
}

func (r *ScheduleRepo) FindByScheduleID(ctx context.Context, scheduleID int, scheduleType string) (*domain.Schedule, error) {
	query := `SELECT RegistroNo, IdTercero, NombreAgenda
	          FROM tblagendas
	          WHERE RegistroNo = ?`
	args := []interface{}{scheduleID}

	// Filter by agenda name matching the procedure type (replicates Laravel ScheduleRepository logic)
	if scheduleType != "" {
		st := strings.ToLower(scheduleType)
		switch st {
		case "sedacion":
			// Sedation: agenda name must contain "sedacion" OR "anestesia"
			query += ` AND (LOWER(NombreAgenda) LIKE '%sedacion%' OR LOWER(NombreAgenda) LIKE '%anestesia%')`
		case "procedimiento", "nocturno":
			// Special types: agenda name MUST contain the keyword
			query += ` AND LOWER(NombreAgenda) LIKE ?`
			args = append(args, "%"+st+"%")
		default:
			// Default types (consulta, examen, etc.): exclude special agendas
			query += ` AND LOWER(NombreAgenda) NOT LIKE '%procedimiento%'
			           AND LOWER(NombreAgenda) NOT LIKE '%nocturno%'
			           AND LOWER(NombreAgenda) NOT LIKE '%sedacion%'
			           AND LOWER(NombreAgenda) NOT LIKE '%anestesia%'`
		}
	}

	var s domain.Schedule
	err := r.db.QueryRowContext(ctx, query, args...).Scan(&s.ID, &s.DoctorDocument, &s.Name)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *ScheduleRepo) FindBookedSlots(ctx context.Context, agendaID int, date string) ([]string, error) {
	// Use LEFT(FechaCita, 8) = YYYYMMDD to match by date, avoiding reliance on FeCita
	// which may have been stored with wrong timezone in legacy records.
	query := `SELECT FechaCita FROM citas
	          WHERE Agenda = ? AND LEFT(FechaCita, 8) = REPLACE(?, '-', '')
	            AND Cancelada = 0 AND (Remonte = 0 OR Remonte IS NULL)`

	rows, err := r.db.QueryContext(ctx, query, agendaID, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var slots []string
	for rows.Next() {
		var timeSlot string
		if err := rows.Scan(&timeSlot); err != nil {
			return nil, err
		}
		slots = append(slots, timeSlot)
	}
	return slots, rows.Err()
}

// FindWorkingDayException checks if a working day exception exists for the given agenda+doctor+date.
func (r *ScheduleRepo) FindWorkingDayException(ctx context.Context, agendaID int, doctorDoc, date string) (*domain.WorkingDay, error) {
	query := `SELECT IdTercero, Fecha, JornadaM, JornadaT, IdAgenda
	          FROM tblexepciondias
	          WHERE IdAgenda = ? AND IdTercero = ? AND Fecha = ?
	          LIMIT 1`

	var d domain.WorkingDay
	var fecha time.Time
	var morningInt, afternoonInt int

	err := r.db.QueryRowContext(ctx, query, agendaID, doctorDoc, date).Scan(
		&d.DoctorDocument, &fecha, &morningInt, &afternoonInt, &d.AgendaID,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	d.Date = fecha.Format("2006-01-02")
	d.MorningEnabled = (morningInt == -1)
	d.AfternoonEnabled = (afternoonInt == -1)
	return &d, nil
}

// UpdateWorkingDayExceptionDate moves a working day exception from oldDate to newDate.
func (r *ScheduleRepo) UpdateWorkingDayExceptionDate(ctx context.Context, agendaID int, doctorDoc, oldDate, newDate string) (bool, error) {
	result, err := r.db.ExecContext(ctx,
		`UPDATE tblexepciondias SET Fecha = ?
		 WHERE IdAgenda = ? AND IdTercero = ? AND Fecha = ?`,
		newDate, agendaID, doctorDoc, oldDate)
	if err != nil {
		return false, err
	}
	rows, _ := result.RowsAffected()
	return rows > 0, nil
}

// DeleteWorkingDayException removes a working day exception for the given agenda+doctor+date.
func (r *ScheduleRepo) DeleteWorkingDayException(ctx context.Context, agendaID int, doctorDoc, date string) (bool, error) {
	result, err := r.db.ExecContext(ctx,
		`DELETE FROM tblexepciondias
		 WHERE IdAgenda = ? AND IdTercero = ? AND Fecha = ?`,
		agendaID, doctorDoc, date)
	if err != nil {
		return false, err
	}
	rows, _ := result.RowsAffected()
	return rows > 0, nil
}

// FindAsuntoForCups no aplica en Antares (datosipsndx no tiene AsuntoPctos).
func (r *ScheduleRepo) FindAsuntoForCups(_ context.Context, _ string) int { return 0 }
