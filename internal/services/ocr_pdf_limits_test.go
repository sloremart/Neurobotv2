package services

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeSizedFile crea un archivo de n bytes (disperso: no ocupa disco) para simular la salida de gs.
func writeSizedFile(t *testing.T, path string, n int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(n); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// Una hoja con un MediaBox enorme (200" a 300 DPI son 60.000 × 60.000 px) deja un JPEG de decenas
// de MB. No se lee a memoria ni se pasa a base64: la conversión se rechaza.
func TestReadPageImages_RejectsOversizedPage(t *testing.T) {
	dir := t.TempDir()
	small := filepath.Join(dir, "order.pdf-p1.jpg")
	huge := filepath.Join(dir, "order.pdf-p2.jpg")
	writeSizedFile(t, small, 1024)
	writeSizedFile(t, huge, maxOCRPageImageBytes+1)

	uris, err := readPageImages([]string{small, huge})
	if err == nil {
		t.Fatalf("una hoja de más de %d bytes debía rechazarse; devolvió %d imágenes", maxOCRPageImageBytes, len(uris))
	}
}

// Varias hojas que una a una caben pero juntas pasan el total también se rechazan.
func TestReadPageImages_RejectsOversizedTotal(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for i := int64(0); i*maxOCRPageImageBytes <= maxOCRImagesTotalBytes; i++ {
		p := filepath.Join(dir, "order.pdf-p"+string(rune('1'+i))+".jpg")
		writeSizedFile(t, p, maxOCRPageImageBytes)
		paths = append(paths, p)
	}
	if _, err := readPageImages(paths); err == nil {
		t.Fatalf("%d hojas de %d bytes pasan el total de %d y debían rechazarse",
			len(paths), maxOCRPageImageBytes, maxOCRImagesTotalBytes)
	}
}

// Las hojas de tamaño normal se leen en orden como data URIs.
func TestReadPageImages_NormalPagesAreRead(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(dir, "order.pdf-p1.jpg")
	p2 := filepath.Join(dir, "order.pdf-p2.jpg")
	writeSizedFile(t, p1, 10)
	writeSizedFile(t, p2, 20)

	uris, err := readPageImages([]string{p1, p2})
	if err != nil {
		t.Fatalf("hojas normales: %v", err)
	}
	if len(uris) != 2 || !strings.HasPrefix(uris[0], "data:image/jpeg;base64,") {
		t.Fatalf("uris = %v", uris)
	}
}

// Ghostscript corre con -dSAFER y con el tope explícito del raster por banda (no depende del valor por
// defecto de la versión instalada).
func TestJPEGRenderArgs_SafeAndBounded(t *testing.T) {
	args := jpegRenderArgs(1, maxOCRPages, "/tmp/x.pdf-p%d.jpg", "/tmp/x.pdf")
	for _, want := range []string{"-dSAFER", "-dMaxBitmap=10000000", "-dFirstPage=1", "-dLastPage=10"} {
		if !slices.Contains(args, want) {
			t.Errorf("falta %s en %v", want, args)
		}
	}
	if args[len(args)-1] != "/tmp/x.pdf" {
		t.Errorf("el PDF debe ir al final: %v", args)
	}
}
