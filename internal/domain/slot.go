package domain

type TimeSlot struct {
	Time           string // HH:mm
	TimeCode       string // YYYYMMDDHHmm
	Date           string // YYYY-MM-DD
	DoctorDocument string
	DoctorName     string
	AgendaID       int
	Available      bool
}
