package domain

import "time"

type Procedure struct {
	ID                 int
	Code               string // Código CUPS
	Name               string
	Description        string
	SpecialtyID        int
	ServiceID          int
	ServiceName        string
	Preparation        string
	Address            string
	VideoURL           string
	AudioURL           string
	Type               string
	RequiredSpaces     int // Slots consecutivos necesarios
	SpecificScheduleID int
	AssignmentFlowID   int
	IsActive           bool
}

type Entity struct {
	ID        int
	Code      string
	Name      string
	PriceType string
	Category  string // CategoriaEntidad (PARTICULAR, EPS, PREPAGADA, etc.)
	IsActive  bool
}

// EntityNameAliases maps internal entity names to user-facing display names.
var EntityNameAliases = map[string]string{
	"SANITAS EVENTO": "SANITAS PREMIUM",
	"SANITAS MRC":    "SANITAS",
}

// DisplayName returns the user-facing name, applying aliases if needed.
func (e Entity) DisplayName() string {
	if alias, ok := EntityNameAliases[e.Name]; ok {
		return alias
	}
	return e.Name
}

// EntityCategories maps index (1-6) to category name matching CategoriaEntidad ENUM.
var EntityCategories = map[int]string{
	1: "PARTICULAR",
	2: "EPS",
	3: "EMPRESA DE MEDICINA PREPAGADA",
	4: "REGIMEN ESPECIAL",
	5: "ARL",
	6: "POLIZA",
}

// EntityCategoryLabels are the user-facing short labels for the WhatsApp list.
var EntityCategoryLabels = map[int]string{
	1: "PARTICULAR",
	2: "EPS",
	3: "PREPAGADA",
	4: "REGIMEN ESPECIAL",
	5: "ARL",
	6: "POLIZA",
}

// ConfirmationEntry es un registro de confirmación de cita (canal + ID de mensaje/llamada).
// Se persiste localmente en confirmation_log ya que SIESA no tiene campos equivalentes
// a los que tenía Antares (MedioConfirmacion, IdMedioConfirmacion).
type ConfirmationEntry struct {
	AppointmentID string
	PatientID     string
	ConfirmedAt   time.Time
	Channel       string // "whatsapp" | "ivr"
	ChannelID     string // Bird message ID o call ID
}

type Municipality struct {
	ID               int
	DepartmentCode   string
	DepartmentName   string
	MunicipalityCode string
	MunicipalityName string
}

// Barrio is a neighborhood from the sis_barrios catalog, scoped by municipality.
type Barrio struct {
	Code string // sis_barrios.codigo (stored in sis_paci.barrio, a bigint)
	Name string // sis_barrios.nombres
	Zone string // sis_barrios.zona (U/R)
}
