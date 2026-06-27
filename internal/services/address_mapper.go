package services

import (
	"strings"

	"github.com/neuro-bot/neuro-bot/internal/domain"
)

// AddressMapper formatea la dirección de una sede con su enlace a Google Maps.
// La URL del mapa llega resuelta por FK (center_location_id → center_locations.google_maps_url);
// este tipo ya NO infiere la URL por coincidencia difusa de texto. Conserva las sedes solo para
// un fallback EXACTO (cuando la URL no viene precargada pero la dirección es canónica).
type AddressMapper struct {
	locations []domain.CenterLocation
}

// NewAddressMapper crea el formateador a partir de las sedes precargadas.
func NewAddressMapper(locations []domain.CenterLocation) *AddressMapper {
	return &AddressMapper{locations: locations}
}

// FormatAddress devuelve la dirección formateada con el enlace a Google Maps.
// mapsURL es la URL resuelta por el FK del procedimiento (determinista); si viene vacía, se intenta
// un emparejamiento EXACTO contra las sedes (la dirección mostrada ya es canónica). Si no hay URL,
// se muestra solo la dirección. Ejemplo:
//
//	"*Dirección:* Calle 34 No 38-47 Barzal\n📍 Ver en Google Maps: https://..."
func (m *AddressMapper) FormatAddress(address, mapsURL string) string {
	if address == "" && len(m.locations) > 0 {
		address = m.locations[len(m.locations)-1].Address
	}
	if mapsURL == "" {
		mapsURL = m.exactMapsURL(address)
	}
	result := "*Dirección:* " + address
	if mapsURL != "" {
		result += "\n📍 Ver en Google Maps: " + mapsURL
	}
	return result
}

// exactMapsURL busca la URL de la sede cuya dirección coincide EXACTAMENTE (normalizada) con la dada.
// Determinista: no usa heurística difusa. Devuelve "" si ninguna coincide.
func (m *AddressMapper) exactMapsURL(address string) string {
	a := strings.TrimSpace(strings.ToLower(address))
	if a == "" {
		return ""
	}
	for _, loc := range m.locations {
		if strings.TrimSpace(strings.ToLower(loc.Address)) == a {
			return loc.GoogleMapsURL
		}
	}
	return ""
}
