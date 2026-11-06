package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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
	args := jpegRenderArgs(1, maxOCRPages, 300, "/tmp/x.pdf-p%d.jpg", "/tmp/x.pdf")
	for _, want := range []string{"-dSAFER", "-r300", "-dMaxBitmap=10000000", "-dFirstPage=1", "-dLastPage=10"} {
		if !slices.Contains(args, want) {
			t.Errorf("falta %s en %v", want, args)
		}
	}
	if args[len(args)-1] != "/tmp/x.pdf" {
		t.Errorf("el PDF debe ir al final: %v", args)
	}
}

// fakeGhostscript simula gs: deja una hoja (p1) del tamaño que diga sizeAt para la resolución pedida y
// anota las resoluciones en el orden en que se pidieron.
func fakeGhostscript(sizeAt func(dpi string) int64, dpis *[]string) gsRunner {
	return func(_ context.Context, args []string) ([]byte, error) {
		var dpi, out string
		for _, a := range args {
			if strings.HasPrefix(a, "-r") {
				dpi = a
			}
			if v, ok := strings.CutPrefix(a, "-sOutputFile="); ok {
				out = v
			}
		}
		*dpis = append(*dpis, dpi)
		f, err := os.Create(fmt.Sprintf(out, 1))
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		return nil, f.Truncate(sizeAt(dpi))
	}
}

// Una hoja carta u oficio cabe a 300 DPI: una sola conversión, igual que siempre.
func TestRenderPDFPages_NormalPageStaysAt300(t *testing.T) {
	var dpis []string
	pdf := filepath.Join(t.TempDir(), "order.pdf")
	uris, err := renderPDFPages(context.Background(), fakeGhostscript(func(string) int64 { return 2 << 20 }, &dpis), pdf, 1, maxOCRPages)
	if err != nil || len(uris) != 1 {
		t.Fatalf("uris=%d err=%v", len(uris), err)
	}
	if !slices.Equal(dpis, []string{"-r300"}) {
		t.Errorf("resoluciones = %v, esperaba solo -r300", dpis)
	}
}

// Una hoja que a 300 DPI pasa el tope (la foto de celular pasada a PDF, página = píxeles) se vuelve a
// convertir a menos DPI y se lee; no se rechaza de entrada.
func TestRenderPDFPages_OversizedPageRetriesAtLowerDPI(t *testing.T) {
	var dpis []string
	pdf := filepath.Join(t.TempDir(), "order.pdf")
	size := func(dpi string) int64 {
		if dpi == "-r300" {
			return 24 << 20
		}
		return 6 << 20
	}
	uris, err := renderPDFPages(context.Background(), fakeGhostscript(size, &dpis), pdf, 1, maxOCRPages)
	if err != nil || len(uris) != 1 {
		t.Fatalf("la hoja debía leerse a menor resolución: uris=%d err=%v", len(uris), err)
	}
	if !slices.Equal(dpis, []string{"-r300", "-r150"}) {
		t.Errorf("resoluciones = %v, esperaba -r300 y -r150", dpis)
	}
	if left, _ := filepath.Glob(pdf + "-p*.jpg"); len(left) != 0 {
		t.Errorf("quedaron temporales: %v", left)
	}
}

// Si ni a la menor resolución cabe, se rechaza.
func TestRenderPDFPages_RejectsWhenNoResolutionFits(t *testing.T) {
	var dpis []string
	pdf := filepath.Join(t.TempDir(), "order.pdf")
	_, err := renderPDFPages(context.Background(), fakeGhostscript(func(string) int64 { return maxOCRPageImageBytes + 1 }, &dpis), pdf, 1, maxOCRPages)
	if !errors.Is(err, errPageImagesTooLarge) {
		t.Fatalf("err = %v, esperaba errPageImagesTooLarge", err)
	}
	if !slices.Equal(dpis, []string{"-r300", "-r150", "-r100"}) {
		t.Errorf("resoluciones = %v", dpis)
	}
}

// Una hoja gigante (el MediaBox de 200") sale a 300 DPI muchas veces por encima del tope: se salta
// directo a la resolución menor, sin pasar por 150.
func TestRenderPDFPages_HugePageJumpsStraightTo100(t *testing.T) {
	var dpis []string
	pdf := filepath.Join(t.TempDir(), "order.pdf")
	size := func(dpi string) int64 {
		if dpi == "-r300" {
			return 9 * maxOCRPageImageBytes
		}
		return 1 << 20
	}
	uris, err := renderPDFPages(context.Background(), fakeGhostscript(size, &dpis), pdf, 1, maxOCRPages)
	if err != nil || len(uris) != 1 {
		t.Fatalf("uris=%d err=%v", len(uris), err)
	}
	if !slices.Equal(dpis, []string{"-r300", "-r100"}) {
		t.Errorf("resoluciones = %v, esperaba -r300 y -r100", dpis)
	}
}

// Toda la conversión de un documento, con todos sus intentos, tiene su propio límite
// (pdfRenderBudget), más corto que el del análisis: lo que sobra queda para Vision.
func TestRenderPDFPages_HasItsOwnBudget(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), defaultAnalyzeTimeout)
	defer cancel()
	var left time.Duration
	run := func(ctx context.Context, _ []string) ([]byte, error) {
		dl, _ := ctx.Deadline()
		left = time.Until(dl)
		return nil, errors.New("gs falso")
	}
	if _, err := renderPDFPages(parent, run, filepath.Join(t.TempDir(), "order.pdf"), 1, maxOCRPages); err == nil {
		t.Fatal("el fallo de gs se devuelve")
	}
	if pdfRenderBudget != 20*time.Second || left <= 0 || left > pdfRenderBudget {
		t.Errorf("plazo de la conversión = %v, límite %v", left, pdfRenderBudget)
	}
}

// Si gs no termina dentro del límite, la conversión falla como un fallo de gs y no espera al plazo
// del análisis.
func TestRenderPDFPages_BudgetExhaustedFailsLikeGhostscript(t *testing.T) {
	var calls int
	run := func(ctx context.Context, _ []string) ([]byte, error) {
		calls++
		<-ctx.Done()
		return nil, ctx.Err()
	}
	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	defer slog.SetDefault(previous)
	start := time.Now()
	_, err := renderPDFPagesWithin(context.Background(), run, 50*time.Millisecond,
		filepath.Join(t.TempDir(), "order.pdf"), 1, maxOCRPages)
	if !strings.Contains(logged.String(), "se agotó el presupuesto de conversión") {
		t.Errorf("el presupuesto agotado deja su línea propia en el registro: %s", logged.String())
	}
	if err == nil || !strings.Contains(err.Error(), "pdf to image conversion failed") || errors.Is(err, errPageImagesTooLarge) {
		t.Fatalf("err = %v, esperaba el fallo de gs", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second || calls != 1 {
		t.Errorf("agotado el límite no se reintenta: %v, %d conversiones", elapsed, calls)
	}
}
