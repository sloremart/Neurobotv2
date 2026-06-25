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

// CupQuantity devuelve la cantidad embebida en el código CUPS: el sufijo numérico si existe
// (ej: "891509-8" → 8), o 1 si no hay sufijo (ej: "891509", "891704PED" → 1). Espeja a
// BaseCupCode: lo que BaseCupCode quita como sufijo, CupQuantity lo interpreta como la cantidad
// de unidades del procedimiento. Validado contra la BD (2026-06-24): el precio en sis_proc_precios
// es plano entre base y variantes (precio UNITARIO) y al facturar la clínica expande "891509-8" en
// 8 filas del CUP base — el sufijo ES la cantidad, no un multiplicador de la columna Cantidad.
func CupQuantity(code string) int {
	if idx := strings.LastIndex(code, "-"); idx > 0 {
		if n, err := strconv.Atoi(code[idx+1:]); err == nil && n > 0 {
			return n
		}
	}
	return 1
}
