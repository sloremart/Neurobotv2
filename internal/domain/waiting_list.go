package domain

import "time"

// WaitingListEntry represents a patient waiting for slot availability.
type WaitingListEntry struct {
	ID                 string
	PhoneNumber        string
	PatientID          string
	PatientDoc         string
	PatientName        string
	PatientAge         int
	PatientGender      string
	PatientEntity      string
	ContractCode       string // sis_paci.contrato — drives MRC limit check in the WL flow
	CupsCode           string
	CupsName           string
	IsContrasted       bool
	IsSedated          bool
	Espacios           int
	ProceduresJSON     string
	ProcedureType      string // cups_procedimientos.tipo (with sedation override)
	GfrCreatinine      float64
	GfrHeightCm        int
	GfrWeightKg        float64
	GfrDiseaseType     string
	GfrCalculated      float64
	IsPregnant         bool
	BabyWeightCat      string
	PreferredDoctorDoc string
	Status             string // waiting, notified, scheduled, declined, expired, duplicate_found
	NotifiedAt         *time.Time
	ResolvedAt         *time.Time
	CreatedAt          time.Time
	ExpiresAt          time.Time
}

// WaitingListFilters for querying the waiting list.
type WaitingListFilters struct {
	Status   string
	CupsCode string
	Phone    string // filter by phone_number (exact match)
	DateFrom string // filter created_at >= YYYY-MM-DD
	DateTo   string // filter created_at <= YYYY-MM-DD
}
