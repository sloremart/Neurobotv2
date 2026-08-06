package api

import (
	"testing"
	"time"
)

// isForeignOutboundMessage decide si un evento outbound lo escribió un AGENTE humano (y no el
// bot): dispara la pausa automática del bot en sesiones activas. Los falsos positivos son graves
// (pausarían el bot con sus propios mensajes), así que la decisión es conservadora. Caso real
// 06-ago 13:41: un reintento de Bird re-entregó 29 min después un evento de un mensaje enviado
// ANTES del reinicio (ID desconocido para el proceso nuevo) y pausó una sesión activa — por eso
// la decisión exige además que el mensaje sea FRESCO (createdAt reciente): la intervención real
// de un agente llega en segundos; un reintento trae el timestamp viejo del mensaje original.
func TestIsForeignOutboundMessage(t *testing.T) {
	fresh := time.Now().UTC().Format(time.RFC3339)
	stale := time.Now().UTC().Add(-25 * time.Minute).Format(time.RFC3339)
	cases := []struct {
		name      string
		own       bool
		status    string
		text      string
		createdAt string
		want      bool
	}{
		{"mensaje del propio bot", true, "accepted", "Hola, ¿en qué puedo ayudarte?", fresh, false},
		{"agente escribe texto libre", false, "accepted", "Buenas, le ayudo con su cita", fresh, true},
		{"agente manda solo imagen (texto vacío)", false, "accepted", "", fresh, true},
		{"comando /bot de agente", false, "accepted", "/bot resume ASK_DOCUMENT", fresh, false},
		{"delivered tardío de mensaje pre-restart", false, "delivered", "texto viejo", fresh, false},
		{"read tardío", false, "read", "", fresh, false},
		{"mensaje puente del bot (interstitial)", false, "accepted", "Te voy a conectar con un agente. Un momento por favor...", fresh, false},
		{"reintento tardío de Bird (mensaje viejo, ID perdido en el reinicio)", false, "sent", "menú del bot", stale, false},
		{"sin createdAt: no hay prueba de frescura → no pausar", false, "accepted", "texto", "", false},
		{"createdAt ilegible → no pausar", false, "accepted", "texto", "no-es-fecha", false},
	}
	for _, tc := range cases {
		if got := isForeignOutboundMessage(tc.own, tc.status, tc.text, tc.createdAt); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
