package api

import "testing"

// isForeignOutboundMessage decide si un evento outbound lo escribió un AGENTE humano (y no el
// bot): dispara la pausa automática del bot en sesiones activas. Los falsos positivos son graves
// (pausarían el bot con sus propios mensajes), así que la decisión es conservadora.
func TestIsForeignOutboundMessage(t *testing.T) {
	cases := []struct {
		name   string
		own    bool
		status string
		text   string
		want   bool
	}{
		{"mensaje del propio bot", true, "accepted", "Hola, ¿en qué puedo ayudarte?", false},
		{"agente escribe texto libre", false, "accepted", "Buenas, le ayudo con su cita", true},
		{"agente manda solo imagen (texto vacío)", false, "accepted", "", true},
		{"comando /bot de agente", false, "accepted", "/bot resume ASK_DOCUMENT", false},
		{"delivered tardío de mensaje pre-restart", false, "delivered", "texto viejo", false},
		{"read tardío", false, "read", "", false},
		{"mensaje puente del bot (interstitial)", false, "accepted", "Te voy a conectar con un agente. Un momento por favor...", false},
	}
	for _, tc := range cases {
		if got := isForeignOutboundMessage(tc.own, tc.status, tc.text); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
