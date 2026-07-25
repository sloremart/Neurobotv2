package domain

import "time"

type Patient struct {
	ID             string
	DocumentType   string
	DocumentNumber string
	FirstName      string
	SecondName     string
	FirstSurname   string
	SecondSurname  string
	FullName       string
	BirthDate      time.Time
	Gender         string // F/M
	Phone          string
	Email          string
	Address        string
	CityCode       string // cod_muni (ej: "001")
	CityName       string // sis_muni.nombre, resolved at lookup for display
	DepartmentCode string // cod_dep (ej: "50"); needed for Sanitas MRC resolution
	Zone           string // U/R
	EntityCode     string
	// ContractCode is sis_paci.contrato ("4","5","6","7",""). For Sanitas (empresa EPS005)
	// it distinguishes MRC (5,6) from Evento (4,7) — the entity code alone cannot. Drives
	// MRC limit validation (IsMRCPatient) and contract resolution at booking.
	ContractCode    string
	AffiliationType string
	UserType        string
	Level           string
	MaritalStatus   string
	EducationLevel  string
	CountryCode     string
}

type CreatePatientInput struct {
	DocumentType    string
	DocumentNumber  string
	FirstName       string
	SecondName      string
	FirstSurname    string
	SecondSurname   string
	BirthDate       time.Time
	Gender          string
	Phone           string
	Phone2          string // celular secundario → sis_paci.telefono_alternativo
	Email           string
	Address         string
	DepartmentCode  string // cod_dep en SIESA (ej: "50")
	CityCode        string // cod_muni en SIESA (ej: "001")
	Zone            string
	EntityCode      string
	AffiliationType string // C=Cotizante, B=Beneficiario, O=Otro → tipo_afilia 1/2/3
	UserType        string // "1"-"9" → tipo_usuario
	Level           string
	MaritalStatus   string // "1"-"5" → estadoCivil
	BloodType       string // sis_tiposangre.valor (ej. "O+") → tipo_sangre
	Barrio          string // sis_barrios.codigo → sis_paci.barrio (bigint)
	EducationLevel  string // id de nivel educativo → sis_paci.escolaridad (int); "" o "0" → NULL
	Occupation      string // ocupación/oficio (texto libre) → sis_paci.ocupacion (varchar 50)
	CountryCode     string
}
