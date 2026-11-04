package services

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// photoPDF arma un PDF de una hoja con una foto de w×h px y la página del tamaño de los píxeles (72
// DPI), como lo dejan las apps que pasan la foto de la orden a PDF.
func photoPDF(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(1)) //nolint:gosec // ruido de prueba, no criptográfico
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := uint8(180 + rng.Intn(60))
			img.Set(x, y, color.RGBA{v, v - uint8(rng.Intn(20)), v, 255})
		}
	}
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}

	var pdf bytes.Buffer
	var offsets []int
	obj := func(body string, stream []byte) {
		offsets = append(offsets, pdf.Len())
		fmt.Fprintf(&pdf, "%d 0 obj\n%s", len(offsets), body)
		if stream != nil {
			pdf.WriteString("\nstream\n")
			pdf.Write(stream)
			pdf.WriteString("\nendstream")
		}
		pdf.WriteString("\nendobj\n")
	}
	content := fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q", w, h)
	pdf.WriteString("%PDF-1.4\n")
	obj("<< /Type /Catalog /Pages 2 0 R >>", nil)
	obj("<< /Type /Pages /Kids [3 0 R] /Count 1 >>", nil)
	obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] /Contents 4 0 R "+
		"/Resources << /XObject << /Im0 5 0 R >> >> >>", w, h), nil)
	obj(fmt.Sprintf("<< /Length %d >>", len(content)), []byte(content))
	obj(fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB "+
		"/BitsPerComponent 8 /Filter /DCTDecode /Length %d >>", w, h, jpg.Len()), jpg.Bytes())
	xref := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return pdf.Bytes()
}

// Con el Ghostscript real (se salta sin gs o con -short): la foto de celular de 4032×3024 pasada a PDF a
// 72 DPI (página de 56×42") deja a 300 DPI un JPEG por encima del tope por hoja; se vuelve a convertir a
// menos DPI y se lee, en vez de rechazarse.
func TestPdfPagesToDataURIs_PhonePhotoPDFIsRead(t *testing.T) {
	if testing.Short() {
		t.Skip("conversión real con Ghostscript")
	}
	if _, err := exec.LookPath("gs"); err != nil {
		t.Skip("gs no está instalado")
	}
	path := filepath.Join(t.TempDir(), "order.pdf")
	if err := os.WriteFile(path, photoPDF(t, 4032, 3024), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultAnalyzeTimeout)
	defer cancel()

	start := time.Now()
	uris, err := pdfPagesToDataURIs(ctx, path, 1, maxOCRPages)
	if err != nil {
		t.Fatalf("la foto pasada a PDF debía leerse: %v", err)
	}
	if len(uris) != 1 {
		t.Fatalf("hojas = %d, esperaba 1", len(uris))
	}
	t.Logf("convertida en %s, data URI de %d bytes", time.Since(start).Round(time.Millisecond), len(uris[0]))
}
