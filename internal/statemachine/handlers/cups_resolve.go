package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/repository"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	sm "github.com/neuro-bot/neuro-bot/internal/statemachine"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// RESOLUCIÓN DE UN CÓDIGO CUPS QUE VALIDATE_OCR NO PUEDE AGENDAR TAL CUAL (decisión del dueño,
// 2026-11-03; tomada de hdg-bot, internal/eligibility/cups_resolve.go). Antes, un código que no estaba
// en el catálogo, o que lo estaba pero con otra especialidad (ocrSpecialtyMismatch), se descartaba en
// silencio. Ahora, SOLO para esos códigos y en cascada:
//
//  0. Un código que EXISTE en el catálogo pero está inactivo es real y Neuro no lo agenda: se descarta,
//     sin cascada (revisión de 2026-11-03: sus hermanos a un dígito son otros estudios reales).
//  1. SEGUNDA LECTURA dedicada del código (OCRService.ReadCupsCode). Si devuelve el MISMO código, la
//     lectura queda confirmada y se descarta como siempre. Si devuelve otro, activo, que pasa la
//     validación de especialidad y cuyo nombre es IGUAL al del procedimiento leído (CompareCupsNames),
//     se corrige y se sigue; si el nombre coincide solo por prefijo, es duda.
//  2. BÚSQUEDA POR NOMBRE en el catálogo: un ÚNICO procedimiento a un dígito del leído (sustitución o
//     transposición) entre los de nombre igual o por prefijo, y con el nombre IGUAL, es una mala
//     lectura: se corrige.
//  3. Hay procedimientos con el mismo nombre (igual o por prefijo) pero ninguno a un dígito, varios,
//     o el único coincide solo por prefijo (singular/plural, que en CUPS separa bilateral de
//     unilateral): DUDA. La orden pasa a un agente con lo leído y los candidatos
//     (cups_posible_mala_lectura). También es duda una corrección a un código que YA está en la orden:
//     la dejaría duplicada y perdería el leído.
//  4. Ninguno con el mismo nombre: la conducta de siempre, se descarta.
//
// La corrección exige las DOS señales (la lectura o el código a un dígito, y el mismo nombre en el
// catálogo), nunca una sola, y nunca es silenciosa: queda en ctxCupsCorrections para el resumen del
// agente y emite `cups_corregido`. El paciente sigue confirmando la lista en CONFIRM_OCR_RESULT.

// CupsCodeReader son las lecturas con el modelo de documento que usa VALIDATE_OCR (las implementa
// services.OCRService): la segunda lectura del código de UN procedimiento (ReadCupsCode, sobre las
// imágenes de la orden que da OrderImages) y la elección por nombre de un procedimiento sin código entre
// los candidatos del catálogo (ChooseCupsByName, solo con confianza alta). Opcional: sin lector se
// saltan los dos pasos.
type CupsCodeReader interface {
	OrderImages(ctx context.Context, documentURL string) ([]string, error)
	ReadCupsCode(ctx context.Context, images []string, procedureName string) (string, error)
	ChooseCupsByName(ctx context.Context, readName string, candidates []services.CupsCandidate) (string, error)
}

const (
	// ctxOCROrderURL es la URL del adjunto del que salieron los CUPS que VALIDATE_OCR va a validar. La
	// deja quien leyó la orden en el mismo paso encadenado y VALIDATE_OCR la borra al salir: vive solo
	// en memoria y nunca se usa para CUPS de otra imagen (ni los que escribe un agente).
	ctxOCROrderURL = "ocr_order_url"
	// ctxCupsCorrections guarda las correcciones de la orden validada (JSON []cupsCorrection), para
	// el resumen del agente.
	ctxCupsCorrections = "ocr_cups_corrections"
	// ctxCupsNameAssignments guarda los códigos asignados por nombre en la orden validada (JSON
	// []cupsCorrection, sin Read), para el resumen del agente (cups_name_assign.go).
	ctxCupsNameAssignments = "ocr_cups_name_assignments"

	// Cómo se corrigió un código (cupsCorrection.Via y el Reason de `cups_corregido`).
	cupsCorrectedBySecondReading = "segunda_lectura"
	cupsCorrectedByName          = "nombre"

	// maxCupsCandidates es cuántos candidatos parecidos lleva la nota de una duda.
	maxCupsCandidates = 3
	// secondReadingReserve es lo que la segunda lectura le deja SIEMPRE al mensaje para persistir y
	// responder: corre dentro del mismo mensaje que ya gastó la lectura de la orden (mismo modo de
	// fallo que el bug de presupuesto del OCR de 2026-07-27).
	secondReadingReserve = 20 * time.Second
)

// cupsCorrection es una corrección hecha en la orden.
type cupsCorrection struct {
	Read string `json:"leido"`
	Used string `json:"usado"`
	Via  string `json:"via"`
	Name string `json:"nombre"`
}

// cupsResolution es el desenlace de la cascada para un código.
type cupsResolution struct {
	proc       *domain.Procedure // el procedimiento con el que sigue; nil = no se corrigió
	via        string
	doubt      bool
	candidates []domain.Procedure // los parecidos, de mayor a menor parecido (máx. maxCupsCandidates)
}

// cupsResolver resuelve los códigos de UNA pasada de VALIDATE_OCR. El catálogo entero y las imágenes
// de la orden se piden una vez y solo si algún código los necesita.
type cupsResolver struct {
	repo     repository.ProcedureRepository
	reader   CupsCodeReader
	orderURL string
	catalog  []domain.Procedure
	loaded   bool
	// images: la orden ya bajada y convertida para la segunda lectura (imagesLoaded = ya se pidió con
	// éxito). Un fallo no se guarda: el siguiente código lo vuelve a intentar, como antes.
	images       []string
	imagesLoaded bool
	// firstVisits: las consultas de primera vez del catálogo, para las interconsultas (cups_name_assign.go).
	firstVisits []domain.Procedure
	// inOrder son los códigos (base) de la orden: los leídos y los ya corregidos.
	inOrder map[string]bool
}

func newCupsResolver(repo repository.ProcedureRepository, reader CupsCodeReader, sess *session.Session, cups []services.CUPSEntry) *cupsResolver {
	inOrder := make(map[string]bool, len(cups))
	for _, c := range cups {
		inOrder[utils.BaseCupCode(strings.TrimSpace(c.Code))] = true
	}
	return &cupsResolver{repo: repo, reader: reader, orderURL: sess.GetContext(ctxOCROrderURL), inOrder: inOrder}
}

// withFreshOrderURL envuelve los pasos que reciben la orden (ASK_MEDICAL_ORDER con la orden guardada,
// UPLOAD_MEDICAL_ORDER, UPLOAD_EMG_ORDER): cada uno empieza sin ctxOCROrderURL y la borra al salir si
// no deja la de su propia foto. Si un encadenado se cortó antes de VALIDATE_OCR, la URL quedó guardada
// en la sesión y alimentaría la segunda lectura de los CUPS de otra imagen.
func withFreshOrderURL(h sm.StateHandler) sm.StateHandler {
	return func(ctx context.Context, sess *session.Session, msg bird.InboundMessage) (*sm.StateResult, error) {
		delete(sess.Context, ctxOCROrderURL)
		res, err := h(ctx, sess, msg)
		if res != nil {
			if _, set := res.UpdateCtx[ctxOCROrderURL]; !set {
				res.WithClearCtx(ctxOCROrderURL)
			}
		}
		return res, err
	}
}

// findActiveProcedure busca el código en el catálogo y, si no está activo y trae sufijo numérico
// (ej "891509-16"), su código base. Devuelve el procedimiento activo (nil si no hay), el código con el
// que se encontró y si el código (o su base) existe en el catálogo aunque esté inactivo.
func findActiveProcedure(ctx context.Context, repo repository.ProcedureRepository, code string) (*domain.Procedure, string, bool) {
	p, _ := repo.FindByCode(ctx, code)
	if p != nil && p.IsActive {
		return p, code, true
	}
	exists := p != nil
	if base := utils.BaseCupCode(code); base != code {
		if bp, err := repo.FindByCode(ctx, base); err == nil && bp != nil {
			if bp.IsActive {
				return bp, base, true
			}
			exists = true
		}
	}
	return nil, code, exists
}

// resolve aplica la cascada a un código que no se puede agendar tal cual (no está en el catálogo o
// choca con la especialidad; los inactivos no llegan aquí).
func (r *cupsResolver) resolve(ctx context.Context, cup services.CUPSEntry) cupsResolution {
	read := utils.BaseCupCode(strings.TrimSpace(cup.Code))

	// Paso 1: segunda lectura dedicada del código.
	if code := r.secondReading(ctx, cup.Name); code == read {
		return cupsResolution{} // confirmado: el código es el que dice la orden
	} else if code != "" {
		if proc, _, _ := findActiveProcedure(ctx, r.repo, code); proc != nil {
			switch r.compare(cup, *proc) {
			case services.CupsNamesEqual:
				return r.correct(*proc, cupsCorrectedBySecondReading)
			case services.CupsNamesByPrefix:
				return cupsResolution{doubt: true, candidates: []domain.Procedure{*proc}}
			}
		}
	}

	// Pasos 2 a 4: el mismo procedimiento por nombre en el catálogo.
	same := r.sameByName(ctx, cup, read)
	if len(same) == 0 {
		return cupsResolution{}
	}
	var oneDigit []domain.Procedure
	for _, p := range same {
		if services.CupsOneDigitApart(read, utils.BaseCupCode(p.Code)) {
			oneDigit = append(oneDigit, p)
		}
	}
	if len(oneDigit) == 1 && r.compare(cup, oneDigit[0]) == services.CupsNamesEqual {
		return r.correct(oneDigit[0], cupsCorrectedByName)
	}
	return cupsResolution{doubt: true, candidates: same[:min(len(same), maxCupsCandidates)]}
}

// compare dice cuánto se parece el procedimiento del catálogo al que leyó el OCR (CompareCupsNames);
// con choque de especialidad, nada.
func (r *cupsResolver) compare(cup services.CUPSEntry, p domain.Procedure) services.CupsNameMatch {
	if ocrSpecialtyMismatch(cup.Name, p.Name) {
		return services.CupsNamesDiffer
	}
	return services.CompareCupsNames(cup.Name, p.Name)
}

// correct corrige al procedimiento, salvo que su código ya esté en la orden: eso es duda.
func (r *cupsResolver) correct(p domain.Procedure, via string) cupsResolution {
	code := utils.BaseCupCode(p.Code)
	if r.inOrder[code] {
		return cupsResolution{doubt: true, candidates: []domain.Procedure{p}}
	}
	r.inOrder[code] = true
	return cupsResolution{proc: &p, via: via}
}

// modelContext acota una llamada al modelo de documento dentro del mensaje: con menos de
// minBudgetForExtraPage no se llama (ok=false) y, si se llama, deja siempre secondReadingReserve para
// persistir y responder.
func modelContext(ctx context.Context) (context.Context, context.CancelFunc, bool) {
	dl, ok := ctx.Deadline()
	if !ok {
		//nolint:gosec // G118: el cancel se DEVUELVE al llamador, que lo invoca con defer
		c, cancel := context.WithCancel(ctx)
		return c, cancel, true
	}
	if time.Until(dl) <= minBudgetForExtraPage {
		return nil, nil, false
	}
	//nolint:gosec // G118: el cancel se DEVUELVE al llamador, que lo invoca con defer
	c, cancel := context.WithDeadline(ctx, dl.Add(-secondReadingReserve))
	return c, cancel, true
}

// secondReading pide la segunda lectura si hay lector, URL de la orden y presupuesto; vacío si no.
func (r *cupsResolver) secondReading(ctx context.Context, name string) string {
	if r.reader == nil || r.orderURL == "" || strings.TrimSpace(name) == "" {
		return ""
	}
	ctx, cancel, ok := modelContext(ctx)
	if !ok {
		slog.Warn("segunda lectura del CUPS omitida: no queda presupuesto en el mensaje")
		return ""
	}
	defer cancel()
	if !r.imagesLoaded {
		images, err := r.reader.OrderImages(ctx, r.orderURL)
		if err != nil {
			slog.Warn("segunda lectura del CUPS: no se pudo bajar la orden", "error", err)
			return ""
		}
		r.images, r.imagesLoaded = images, true
	}
	code, err := r.reader.ReadCupsCode(ctx, r.images, name)
	if err != nil {
		slog.Warn("segunda lectura del CUPS falló", "error", err)
		return ""
	}
	return utils.BaseCupCode(strings.TrimSpace(code))
}

// sameByName devuelve los procedimientos activos con el nombre del leído, igual o por prefijo (compare),
// sin el propio código leído (ya rechazado), en el orden del catálogo. Un fallo al leer el catálogo deja la
// conducta de siempre (se descarta).
func (r *cupsResolver) sameByName(ctx context.Context, cup services.CUPSEntry, read string) []domain.Procedure {
	var out []domain.Procedure
	for _, p := range r.activeCatalog(ctx) {
		if utils.BaseCupCode(p.Code) != read && r.compare(cup, p) != services.CupsNamesDiffer {
			out = append(out, p)
		}
	}
	return out
}

// activeCatalog es el catálogo activo, pedido una vez por pasada y solo si algún código lo necesita.
func (r *cupsResolver) activeCatalog(ctx context.Context) []domain.Procedure {
	if !r.loaded {
		r.loaded = true
		all, err := r.repo.FindAllActive(ctx)
		if err != nil {
			slog.Warn("resolución del CUPS: no se pudo leer el catálogo", "error", err)
		}
		r.catalog = all
	}
	return r.catalog
}

// cupsDoubtNote es la nota que lee el agente en una duda: el código y el nombre leídos y lo que el
// catálogo trae con nombre parecido. Sin datos del paciente.
func cupsDoubtNote(cup services.CUPSEntry, candidates []domain.Procedure) string {
	parts := make([]string, 0, len(candidates))
	for _, c := range candidates {
		parts = append(parts, fmt.Sprintf("%s «%s»", c.Code, c.Name))
	}
	return fmt.Sprintf("POSIBLE MALA LECTURA DEL CUPS: el OCR leyó %s «%s», que no se puede agendar tal cual. "+
		"El catálogo trae con el mismo nombre: %s. Verifica la orden y devuelve los CUPS correctos.",
		cup.Code, oneLine(cup.Name), strings.Join(parts, "; "))
}

// keptCorrections junta las correcciones (o asignaciones) anteriores de la clave key que siguen en la
// orden (una página añadida vuelve a pasar por VALIDATE_OCR con los códigos ya resueltos) con las de esta
// pasada.
func keptCorrections(sess *session.Session, key string, fresh []cupsCorrection, cups []services.CUPSEntry) []cupsCorrection {
	var prev []cupsCorrection
	_ = json.Unmarshal([]byte(sess.GetContext(key)), &prev)
	codes := make([]string, 0, len(cups))
	for _, c := range cups {
		codes = append(codes, c.Code)
	}
	var out []cupsCorrection
	for _, c := range append(prev, fresh...) {
		if slices.Contains(codes, c.Used) && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// cupsCorrectionsLine es la línea del resumen del agente con las correcciones de la orden.
func cupsCorrectionsLine(sess *session.Session) string {
	var list []cupsCorrection
	if err := json.Unmarshal([]byte(sess.GetContext(ctxCupsCorrections)), &list); err != nil {
		return ""
	}
	parts := make([]string, 0, len(list))
	for _, c := range list {
		via := "por nombre en el catálogo"
		if c.Via == cupsCorrectedBySecondReading {
			via = "por segunda lectura"
		}
		parts = append(parts, oneLine(fmt.Sprintf("%s leído → %s (%s) · %s", c.Read, c.Used, via, c.Name)))
	}
	return strings.Join(parts, "; ")
}
