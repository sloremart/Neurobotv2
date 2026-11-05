package services

import "testing"

// Revisión del lote C, ronda 3 (R3-M3): descartar lo igual perdía la cantidad, la sedación, el contraste y las
// observaciones de la lectura descartada, y eso cambia los cupos. Si difieren, entra como duda.
func TestAddUncodedFrom_DifferentDataIsADoubt(t *testing.T) {
	const firstName = "RX RODILLA"
	first := func() *OCRResult {
		return &OCRResult{Cups: []CUPSEntry{{Code: "873420", Name: firstName, Quantity: 1}}}
	}
	cases := []struct {
		name  string
		joint CUPSEntry
	}{
		{"cantidad, otra redacción", CUPSEntry{Name: "RX RODILLA AP Y LATERAL", Quantity: 4}},
		{"cantidad, el mismo nombre", CUPSEntry{Name: firstName, Quantity: 3}},
		{"sedación", CUPSEntry{Name: firstName, Quantity: 1, IsSedated: true}},
		{"contraste marcado", CUPSEntry{Name: firstName, Quantity: 1, IsContrasted: true}},
		{"observaciones", CUPSEntry{Name: firstName, Quantity: 1, Observations: "BILATERAL"}},
		{"cantidad escrita en el nombre", CUPSEntry{Name: "RX RODILLA #4", Quantity: 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dst := first()
			addUncodedFrom(dst, &OCRResult{Cups: []CUPSEntry{c.joint}})
			if len(dst.Cups) != 2 {
				t.Fatalf("no debe perderse: %+v", dst.Cups)
			}
			if got := dst.Cups[1]; got.Code != "" || got.ReadDoubtWith != firstName {
				t.Fatalf("debe quedar en duda frente a la otra hoja: %+v", got)
			}
		})
	}
}

// La EMG de una extremidad frente a la de cuatro va al agente, con sus dos cantidades intactas.
func TestAddUncodedFrom_EMGQuantityIsADoubt(t *testing.T) {
	dst := &OCRResult{Cups: []CUPSEntry{{Code: "930860", Name: "EMG", Quantity: 1}}}
	addUncodedFrom(dst, &OCRResult{Cups: []CUPSEntry{{Name: "EMG EXTREMIDADES", Quantity: 4}}})
	if len(dst.Cups) != 2 || dst.Cups[0].Quantity != 1 || dst.Cups[1].Quantity != 4 || dst.Cups[1].ReadDoubtWith != "EMG" {
		t.Fatalf("cantidades o duda: %+v", dst.Cups)
	}
}

// Dentro de la lectura conjunta: el mismo texto con otros datos es duda; con los mismos datos, entra una vez.
func TestAddUncodedFrom_SameNameInJointRead(t *testing.T) {
	first := func() *OCRResult { return &OCRResult{Cups: []CUPSEntry{{Code: "879301", Name: "TOMOGRAFIA DE TORAX"}}} }

	dup := first()
	addUncodedFrom(dup, &OCRResult{Cups: []CUPSEntry{
		{Name: "RM RODILLA", Quantity: 3, Observations: "Bilateral"},
		{Name: "rm rodilla", Quantity: 3, Observations: " BILATERAL "},
	}})
	if len(dup.Cups) != 2 || dup.Cups[1].ReadDoubtWith != "" {
		t.Fatalf("duplicado con los mismos datos: %+v", dup.Cups)
	}

	diff := first()
	addUncodedFrom(diff, &OCRResult{Cups: []CUPSEntry{
		{Name: "RM RODILLA", Quantity: 1},
		{Name: "RM RODILLA", Quantity: 3},
		{Name: "RM RODILLA", Quantity: 3},
		{Name: "rm rodilla", Quantity: 1, IsContrasted: true},
	}})
	if len(diff.Cups) != 4 {
		t.Fatalf("cada lectura distinta entra una vez: %+v", diff.Cups)
	}
	for _, c := range diff.Cups[2:] {
		if c.ReadDoubtWith == "" {
			t.Fatalf("con otros datos es duda: %+v", c)
		}
	}

	// También cuando el nombre no se puede comparar.
	vague := first()
	addUncodedFrom(vague, &OCRResult{Cups: []CUPSEntry{{Name: "CONSULTA ESPECIALISTA", Quantity: 1}, {Name: "CONSULTA ESPECIALISTA", Quantity: 2}}})
	if len(vague.Cups) != 3 || vague.Cups[2].ReadDoubtWith == "" {
		t.Fatalf("nombre sin palabra distintiva: %+v", vague.Cups)
	}

	// La marca en el nombre es otra lectura: las dos entran, no se pierde ninguna.
	marks := first()
	addUncodedFrom(marks, &OCRResult{Cups: []CUPSEntry{{Name: "RX RODILLA #1"}, {Name: "RX RODILLA #4"}}})
	if len(marks.Cups) != 3 || marks.Cups[2].ReadDoubtWith == "" {
		t.Fatalf("marcas de cantidad: %+v", marks.Cups)
	}
}

// Con los mismos datos, lo igual se sigue descartando: sin cantidad escrita (0) vale 1.
func TestAddUncodedFrom_SameDataIsDiscarded(t *testing.T) {
	cases := []struct{ name, first, joint string }{
		{"mismo nombre", "RM CEREBRO", "RM CEREBRO"},
		{"otra redacción", "RM CEREBRO", "RESONANCIA MAGNETICA DE CEREBRO"},
	}
	for _, c := range cases {
		for _, q := range [][2]int{{0, 1}, {1, 0}, {0, 0}, {2, 2}} {
			dst := &OCRResult{Cups: []CUPSEntry{{Code: "883101", Name: c.first, Quantity: q[0], Observations: "urgente"}}}
			addUncodedFrom(dst, &OCRResult{Cups: []CUPSEntry{{Name: c.joint, Quantity: q[1], Observations: "URGENTE "}}})
			if len(dst.Cups) != 1 {
				t.Errorf("%s %v: debía descartarse: %+v", c.name, q, dst.Cups)
			}
		}
	}
}
