package utils

import (
	"strconv"
	"strings"
)

// BaseCupCode extrae el código CUPS base quitando el sufijo numérico SIESA.
// SIESA almacena los procedimientos con la cantidad embebida en el código
// (ej: "891509-16" = neuroconducción × 16 nervios). Antares solo tiene "891509".
func BaseCupCode(code string) string {
	if idx := strings.LastIndex(code, "-"); idx > 0 {
		if _, err := strconv.Atoi(code[idx+1:]); err == nil {
			return code[:idx]
		}
	}
	return code
}
