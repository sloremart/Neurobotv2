package nectar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// EL HORARIO Y LOS FESTIVOS SE LEEN DE NÉCTAR (port de hdg-bot, internal/nectar/schedule.go).
//
// Néctar administra los festivos por workspace y el horario por equipo; el bot decide con ellos
// si atiende y qué hace con una escalación (asesores, cola del próximo día hábil). Esta es la
// LECTURA y nada más: validarla y convertirla en horario es de config
// (BusinessHoursFromSchedule), y aplicarla del refrescador (cmd/server/schedule_nectar.go).

// SchedulePath es la ruta de la lectura. Sin conversación: la clave de servicio identifica la
// entidad del bot.
const SchedulePath = "/api/bot/schedule"

// ErrScheduleUnknownChannel es el 404 de esta ruta, que NO es el ErrNotMine de las rutas de
// conversación: aquí es «ese channel_id no es de tu entidad» (configuración mal puesta).
var ErrScheduleUnknownChannel = errors.New("nectar: channel_id ajeno a la entidad del bot (404)")

// ErrScheduleNotModified es el 304 de la lectura CONDICIONAL: el horario publicado sigue siendo
// el que el bot tiene. No es un fallo, pero se devuelve como error porque el Schedule que lo
// acompaña viene VACÍO: un `nil` invitaría a aplicarlo y dejaría al bot cerrado toda la semana.
var ErrScheduleNotModified = errors.New("nectar: el horario no cambió (304)")

// ScheduleWindow es una ventana tal como la publica Néctar. Weekday es ISO: 1 = lunes … 7 =
// domingo. From/To en "HH:MM" 24 h, con To EXCLUSIVO y "24:00" válido como fin del día.
type ScheduleWindow struct {
	Weekday int    `json:"weekday"`
	From    string `json:"from"`
	To      string `json:"to"`
}

// ScheduleHoliday es un día cerrado entero ("YYYY-MM-DD" en la zona de la respuesta); Name es
// informativo.
type ScheduleHoliday struct {
	Date string `json:"date"`
	Name string `json:"name"`
}

// Schedule es la respuesta completa. GeneratedAt viaja como TEXTO a propósito: es informativo, y
// parseado, un formato inesperado tumbaría la decodificación entera.
type Schedule struct {
	Timezone    string            `json:"timezone"`
	Version     string            `json:"version"`
	GeneratedAt string            `json:"generated_at"`
	Windows     []ScheduleWindow  `json:"windows"`
	Holidays    []ScheduleHoliday `json:"holidays"`
}

// GetSchedule lee GET /api/bot/schedule[?channel_id=]. channelID 0 = sin filtro (todos los canales
// de la entidad). Misma clave de servicio y firma de ida que el resto (sobre la URL COMPLETA, así
// que la query queda firmada) y ningún reintento: quien refresca conserva la última lectura buena.
//
// version es la de la ÚLTIMA lectura aplicada ("" mientras rija el respaldo). Con ella la
// petición va CONDICIONAL (`If-None-Match`, entrecomillada porque es un ETag) y un horario que no
// cambió vuelve como 304 sin cuerpo (ErrScheduleNotModified). Un 304 que nadie pidió es un error.
func (c *Client) GetSchedule(ctx context.Context, channelID int64, version string) (Schedule, error) {
	path := SchedulePath
	if channelID != 0 {
		path += "?channel_id=" + strconv.FormatInt(channelID, 10)
	}
	var headers map[string]string
	if version != "" {
		headers = map[string]string{"If-None-Match": quoteETag(version)}
	}
	resp, err := c.exchangeWithHeaders(ctx, http.MethodGet, path, nil, headers)
	if err != nil {
		return Schedule{}, err
	}
	switch {
	case resp.status == http.StatusNotModified && version != "":
		return Schedule{}, ErrScheduleNotModified
	case resp.status == http.StatusNotFound:
		return Schedule{}, fmt.Errorf("%w (channel_id=%d)", ErrScheduleUnknownChannel, channelID)
	case resp.status == http.StatusUnauthorized:
		return Schedule{}, ErrUnauthorized
	case resp.status != http.StatusOK:
		return Schedule{}, fmt.Errorf("nectar: GET %s: status %d %s", path, resp.status,
			strings.TrimSpace(string(resp.body)))
	}
	var out Schedule
	if err := json.Unmarshal(resp.body, &out); err != nil {
		return Schedule{}, fmt.Errorf("nectar: leer respuesta de %s: %w", path, err)
	}
	return out, nil
}

// quoteETag entrecomilla la versión para el If-None-Match; una que ya venga entrecomillada se
// deja como está (doblar las comillas la volvería otra).
func quoteETag(version string) string {
	if strings.HasPrefix(version, `"`) && strings.HasSuffix(version, `"`) && len(version) > 1 {
		return version
	}
	return `"` + version + `"`
}
