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
	CityCode       string
	Zone           string // U/R
	EntityCode     string
	AffiliationType string
	UserType       string
	Occupation     string
	Level          string
	MaritalStatus  string
	BirthPlace     string
	EducationLevel string
	CountryCode    string
}

type CreatePatientInput struct {
	DocumentType       string
	DocumentNumber     string
	DocumentIssuePlace string
	FirstName          string
	SecondName         string
	FirstSurname       string
	SecondSurname      string
	BirthDate          time.Time
	Gender             string
	Phone              string
	Email              string
	Address            string
	DepartmentCode     string // cod_dep en SIESA (ej: "50")
	CityCode           string // cod_muni en SIESA (ej: "001")
	Zone               string
	EntityCode         string
	AffiliationType    string // C=Cotizante, B=Beneficiario, O=Otro → tipo_afilia 1/2/3
	UserType           string // "1"-"9" → tipo_usuario
	Occupation         string
	Level              string
	MaritalStatus      string // "1"-"6" → estadoCivil
	BirthPlace         string
	EducationLevel     string
	CountryCode        string
}
