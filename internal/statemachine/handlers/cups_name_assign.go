package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/neuro-bot/neuro-bot/internal/domain"
	"github.com/neuro-bot/neuro-bot/internal/services"
	"github.com/neuro-bot/neuro-bot/internal/session"
	"github.com/neuro-bot/neuro-bot/internal/utils"
)

// ASIGNACIÓN DEL CÓDIGO POR NOMBRE EN VALIDATE_OCR (decisión del dueño, 2026-11-03). El OCR ya no elige
// un código de la lista del catálogo: si la orden no lo trae, devuelve el nombre tal cual y aquí se
// resuelve (reglas en services/cups_name.go). Va al INICIO de VALIDATE_OCR porque es el embudo de toda
// orden leída (foto, orden guardada, página añadida, orden de EMG): así un CUPS sin código nunca llega a
// la cobertura, y lo asignado pasa por la misma validación del catálogo que un código leído. Lo que no se
// asigna con seguridad va al agente con el motivo de siempre de la orden sin código (ocr_sin_codigo).

// firstVisitSubjects son los asuntos de SIESA de las consultas de primera vez (fisiatría, neurología y
// neuropediatría): lo único que se le ofrece a una interconsulta.
var firstVisitSubjects = map[int]bool{1: true, 8: true, 10: true}

// assignByName resuelve el código de un procedimiento leído sin código.
func (r *cupsResolver) assignByName(ctx context.Context, cup services.CUPSEntry) services.CupsNameResolution {
	catalog := r.activeCatalog(ctx)
	if cup.IsInterconsulta {
		catalog = r.firstVisitCatalog(ctx)
	}
	candidates := make([]services.CupsCandidate, 0, len(catalog))
	for _, p := range catalog {
		candidates = append(candidates, services.CupsCandidate{Code: p.Code, Name: p.Name})
	}
	var choose func(string, []services.CupsCandidate) string
	if r.reader != nil {
		choose = func(read string, shortlist []services.CupsCandidate) string {
			mctx, cancel, ok := modelContext(ctx)
			if !ok {
				slog.Warn("asignación por nombre omitida: no queda presupuesto en el mensaje")
				return ""
			}
			defer cancel()
			code, err := r.reader.ChooseCupsByName(mctx, read, shortlist)
			if err != nil {
				slog.Warn("asignación por nombre: el modelo falló", "error", err)
				return ""
			}
			return code
		}
	}
	res := services.ResolveCupsByName(cup.Name, candidates, cup.IsInterconsulta, choose)
	// Un código que ya está en la orden dejaría el procedimiento duplicado: es duda.
	if res.Code != "" {
		code := utils.BaseCupCode(res.Code)
		if r.inOrder[code] {
			res.Code, res.Name, res.Via = "", "", ""
		} else {
			r.inOrder[code] = true
		}
	}
	return res
}

// firstVisitCatalog son las consultas de primera vez del catálogo activo: las consultas por nombre y,
// de ellas, las de un asunto de primera vez (el asunto se pregunta solo para esas pocas).
func (r *cupsResolver) firstVisitCatalog(ctx context.Context) []domain.Procedure {
	if r.firstVisits != nil {
		return r.firstVisits
	}
	r.firstVisits = []domain.Procedure{}
	for _, p := range r.activeCatalog(ctx) {
		if !strings.Contains(strings.ToUpper(p.Name), "CONSULTA") {
			continue
		}
		if subject, err := r.repo.FindSubjectTypeForCups(ctx, p.Code); err == nil && firstVisitSubjects[subject] {
			r.firstVisits = append(r.firstVisits, p)
		}
	}
	return r.firstVisits
}

// cupsNameDoubtNote es la nota del agente para un procedimiento sin código que no se asignó: el nombre
// leído y los candidatos del catálogo. Sin datos del paciente.
func cupsNameDoubtNote(name string, candidates []services.CupsCandidate) string {
	if len(candidates) == 0 {
		return fmt.Sprintf("PROCEDIMIENTO SIN CÓDIGO: la orden dice «%s» y el catálogo no trae ninguno parecido. "+
			"Verifica la orden y devuelve los CUPS correctos.", oneLine(name))
	}
	parts := make([]string, 0, len(candidates))
	for _, c := range candidates {
		parts = append(parts, fmt.Sprintf("%s «%s»", c.Code, c.Name))
	}
	return fmt.Sprintf("PROCEDIMIENTO SIN CÓDIGO: la orden dice «%s» y no se le pudo asignar un código con seguridad. "+
		"Candidatos del catálogo: %s. Verifica la orden y devuelve los CUPS correctos.", oneLine(name), strings.Join(parts, "; "))
}

// cupsConflictNote es la nota del agente para un CUPS con código cuyo nombre leído contradice el del
// catálogo (guarda de VALIDATE_OCR).
func cupsConflictNote(cup services.CUPSEntry, catalogName string) string {
	return fmt.Sprintf("POSIBLE MALA LECTURA DEL CUPS: el OCR leyó %s «%s», pero en el catálogo %s es «%s» y los "+
		"nombres se contradicen. Verifica la orden y devuelve los CUPS correctos.",
		cup.Code, oneLine(cup.Name), cup.Code, catalogName)
}

// cupsAssignmentsLine es la línea del resumen del agente con los códigos asignados por nombre.
func cupsAssignmentsLine(sess *session.Session) string {
	var list []cupsCorrection
	if err := json.Unmarshal([]byte(sess.GetContext(ctxCupsNameAssignments)), &list); err != nil {
		return ""
	}
	parts := make([]string, 0, len(list))
	for _, c := range list {
		via := "elegido por el modelo"
		if c.Via == services.CupsNameViaEqual {
			via = "nombre igual en el catálogo"
		}
		parts = append(parts, oneLine(fmt.Sprintf("%s ← «%s» (%s)", c.Used, c.Name, via)))
	}
	return strings.Join(parts, "; ")
}
