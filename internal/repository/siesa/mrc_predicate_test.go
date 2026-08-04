package siesa

import (
	"reflect"
	"testing"
)

// Auditoría queries P7: el predicado del grupo MRC debe ser sargable — nada de
// LEFT(CHARINDEX(...)) sobre la columna. El match sigue siendo por CUP BASE (espeja
// IsMRCGroupCups): cp pertenece al grupo si es exactamente una base o una variante
// `base-<sufijo>`. Las entradas con sufijo del catálogo se normalizan a su base y se
// deduplican (una entrada "891402-1" cuenta como su base "891402").
func TestMRCGroupPredicate(t *testing.T) {
	cases := []struct {
		name       string
		cups       []string
		wantClause string
		wantArgs   []interface{}
	}{
		{
			name:       "base_simple",
			cups:       []string{"890274"},
			wantClause: "(cp.id_procedimiento = @p4 OR cp.id_procedimiento LIKE @p5)",
			wantArgs:   []interface{}{"890274", "890274-%"},
		},
		{
			name:       "variantes_se_normalizan_y_deduplican",
			cups:       []string{"891402", "891402-1", "891402PED"},
			wantClause: "(cp.id_procedimiento = @p4 OR cp.id_procedimiento LIKE @p5 OR cp.id_procedimiento = @p6 OR cp.id_procedimiento LIKE @p7)",
			wantArgs:   []interface{}{"891402", "891402-%", "891402PED", "891402PED-%"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clause, args := mrcGroupPredicate(c.cups, 4)
			if clause != c.wantClause {
				t.Errorf("clause = %s\nwant     %s", clause, c.wantClause)
			}
			if !reflect.DeepEqual(args, c.wantArgs) {
				t.Errorf("args = %v, want %v", args, c.wantArgs)
			}
		})
	}
}
