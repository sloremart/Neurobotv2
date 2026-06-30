package siesa

import (
	"reflect"
	"testing"
)

// TestMuniSearchTokens verifica la tokenización del input de municipio: separadores varios → palabras,
// descarta conectores ("y") y de 1 carácter, conserva partes de nombres reales ("del"), cap de 6.
func TestMuniSearchTokens(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"Villavicencio Meta", []string{"villavicencio", "meta"}},                                  // espacio
		{"Villavicencio - Meta", []string{"villavicencio", "meta"}},                                // guion
		{"Villavicencio, Meta", []string{"villavicencio", "meta"}},                                 // coma
		{"Villavicencio y Meta", []string{"villavicencio", "meta"}},                                // conector "y" descartado
		{"San José del Guaviare Guaviare", []string{"san", "josé", "del", "guaviare", "guaviare"}}, // multi-palabra, conserva "del"
		{"Cali Valle del Cauca", []string{"cali", "valle", "del", "cauca"}},
		{"  Bogotá  D.C.  ", []string{"bogotá"}}, // '.' → espacio; "d" y "c" (1 char) descartados → solo "bogotá"
		{"", nil},
	}
	for _, c := range cases {
		if got := muniSearchTokens(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("muniSearchTokens(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
