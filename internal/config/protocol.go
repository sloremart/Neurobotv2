package config

import (
	"fmt"
	"strconv"
	"strings"
)

// --- La bandera del protocolo de escalado v1 de Néctar ---
//
// UNA SOLA PALANCA y gobierna la EMISIÓN, nada más: con 0 el sobre del escalado sale
// exactamente como hoy, byte a byte; con 1 sale en v1. LA RECEPCIÓN NO LA GOBIERNA: el bot
// acepta el dialecto y v1 a la vez, en la misma sesión, con la bandera en cualquier valor —
// ver ReceptionIsDual.

const (
	// ProtocolEscaladoV0 es el dialecto de hoy. Es el DEFAULT: nada se enciende en producción
	// hasta que Néctar entregue sus volcados v1, porque v1 retira `bot_return_menu` del sobre y
	// una interfaz que aún no lea `input` dejaría al agente sin ningún botón de retorno, sin
	// error en ninguna parte.
	ProtocolEscaladoV0 = 0
	// ProtocolEscaladoV1 es el protocolo v1 del acta.
	ProtocolEscaladoV1 = 1
)

// ReceptionIsDual deja escrito en el binario que la recepción es dual y NO se apaga nunca. Es
// una constante y no una variable de entorno a propósito — una bandera que la apagara sería la
// forma de perder un retorno legítimo del agente el día del despliegue, que es exactamente lo
// que la ventana dual viene a evitar. El registro de capacidades la publica.
const ReceptionIsDual = true

// ProtocolVersion es la versión del protocolo de escalado que el bot EMITE: lo que los
// emisores (el sobre del escalado, el descriptor, el registro de capacidades) consultan para
// decidir su forma. Se lee por función y no por el campo suelto para que el sitio donde se
// decide sea uno solo el día que la palanca deje de ser un entero del `.env`.
func (c *Config) ProtocolVersion() int { return c.ProtocolEscaladoVersion }

// EmitsProtocolV1 dice si el bot emite v1. Azúcar sobre ProtocolVersion, para que los emisores
// no repartan comparaciones con el literal 1 por todo el árbol.
func (c *Config) EmitsProtocolV1() bool { return c.ProtocolVersion() == ProtocolEscaladoV1 }

// parseProtocolEscaladoVersion lee PROTOCOL_ESCALADO_VERSION: vacía = el dialecto de hoy, y
// CUALQUIER otro valor que no sea 0 ni 1 tumba el arranque.
//
// NO se lee con getEnvInt (config.go): ese ignora el error de Atoi, así que `=v1` arrancaría
// con 0 sin decir nada — el bot hablaría dialecto creyendo que habla v1, y el desenlace (un
// agente sin botones de retorno) no tiene traza. Lo que no se entendió se ve al arrancar.
func parseProtocolEscaladoVersion(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ProtocolEscaladoV0, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("versión %q no numérica (valores: 0 = dialecto actual, 1 = v1)", raw)
	}
	if v != ProtocolEscaladoV0 && v != ProtocolEscaladoV1 {
		return 0, fmt.Errorf("versión %d desconocida (valores: 0 = dialecto actual, 1 = v1)", v)
	}
	return v, nil
}
