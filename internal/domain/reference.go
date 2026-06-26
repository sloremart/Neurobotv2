package domain

// Tipos de referencia de SIESA expuestos (solo lectura) al dashboard para poblar selectores
// del módulo de catálogo (asociar médicos a CUPS, elegir asunto). Son catálogos pequeños y
// casi estáticos → se consultan con WITH (NOLOCK) y se cachean en el bot.

// MedicoRef es un médico de SIESA (sis_medi) para el selector "asociar médico a CUPS".
type MedicoRef struct {
	Codigo int    `json:"codigo"` // sis_medi.codigo (cod_medi) — el id que se guarda en cups_medico.medico_id
	Cedula string `json:"cedula"`
	Nombre string `json:"nombre"`
}

// AsuntoRef es un asunto de SIESA (sis_asunto) para el selector "asunto del CUPS".
type AsuntoRef struct {
	ID     int    `json:"id"` // sis_asunto.id — el valor que se guarda en cups_procedimientos.asunto_id
	Nombre string `json:"nombre"`
}
