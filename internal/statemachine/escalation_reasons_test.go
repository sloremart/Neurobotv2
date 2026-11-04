package statemachine

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/neuro-bot/neuro-bot/internal/bird"
)

// Néctar valida el detalle con ^[a-z_]{1,64}$ y, si no casa, lo descarta con ERROR en producción.
func TestEscalationReasons_MatchTheWireShape(t *testing.T) {
	shape := regexp.MustCompile(`^[a-z_]{1,64}$`)
	seen := map[string]bool{}
	for _, reason := range EscalationReasons() {
		if !shape.MatchString(reason) {
			t.Errorf("el motivo %q no casa con ^[a-z_]{1,64}$", reason)
		}
		if seen[reason] {
			t.Errorf("el motivo %q está repetido", reason)
		}
		seen[reason] = true
	}
	for _, want := range []string{
		EscalationReasonBotDisabled, EscalationReasonAgentInputRejected, EscalationReasonNotifFreeText,
	} {
		if !seen[want] {
			t.Errorf("EscalationReasons() no incluye %q", want)
		}
	}
}

func TestValidateWithRetry_ExhaustedWritesMaxRetriesReason(t *testing.T) {
	sess := helperSess(StateAskDocument)
	sess.RetryCount = maxRetries - 1
	res := ValidateWithRetry(sess, "x", func(string) bool { return false }, "error")
	assertMaxRetries(t, res)
}

func TestValidateButtonResponse_ExhaustedWritesMaxRetriesReason(t *testing.T) {
	sess := helperSess(StateMainMenu)
	sess.RetryCount = maxRetries - 1
	res, _ := ValidateButtonResponse(sess, bird.InboundMessage{Text: "zzz"}, "a", "b")
	assertMaxRetries(t, res)
}

func TestRetryOrEscalate_ExhaustedWritesMaxRetriesReason(t *testing.T) {
	sess := helperSess(StateAskDocument)
	sess.RetryCount = maxRetries - 1
	assertMaxRetries(t, RetryOrEscalate(sess, "error"))
}

func assertMaxRetries(t *testing.T, res *StateResult) {
	t.Helper()
	if res == nil || res.NextState != StateEscalateToAgent {
		t.Fatalf("esperaba escalación, got %+v", res)
	}
	if got := res.UpdateCtx[CtxEscalationReason]; got != "max_reintentos" {
		t.Fatalf("motivo = %q, quería max_reintentos", got)
	}
}

func TestEscalationKeywordsInterceptor_WritesPatientAskedReason(t *testing.T) {
	sess := helperSess(StateAskDocument)
	res, handled := EscalationKeywordsInterceptor()(context.Background(), sess, bird.InboundMessage{Text: "asesor"})
	if !handled || res == nil {
		t.Fatal("esperaba escalación por keyword")
	}
	if got := res.UpdateCtx[CtxEscalationReason]; got != "paciente_pidio_asesor" {
		t.Fatalf("motivo = %q", got)
	}
	if got := res.UpdateCtx[CtxPatientAskedForAgent]; got != "1" {
		t.Fatalf("falta la marca del paciente: %q", got)
	}
}

// EscalationReasons() no puede olvidar una constante: se recorre el fuente y se compara.
func TestEscalationReasons_ListsEveryConstant(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "escalation_reasons.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	listed := EscalationReasons()
	found := 0
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "EscalationReason") {
					continue
				}
				found++
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok {
					t.Fatalf("%s no es un literal", name.Name)
				}
				if !slices.Contains(listed, strings.Trim(lit.Value, `"`)) {
					t.Errorf("%s (%s) no está en EscalationReasons()", name.Name, lit.Value)
				}
			}
		}
	}
	if found != len(listed) {
		t.Errorf("el fuente declara %d constantes y EscalationReasons() lista %d", found, len(listed))
	}
}

// Cada motivo dice si es un ciclo del paciente (suma al tope de escalaciones) o una escalación que
// dispara el bot (no suma): uno nuevo sin clasificar rompe aquí.
func TestEscalationReasons_EveryReasonIsClassifiedForTheCap(t *testing.T) {
	for _, reason := range EscalationReasons() {
		if _, ok := escalationReasonFromPatient[reason]; !ok {
			t.Errorf("el motivo %q no dice si suma al tope de escalaciones", reason)
		}
	}
	if len(escalationReasonFromPatient) != len(EscalationReasons()) {
		t.Errorf("la clasificación tiene %d motivos y EscalationReasons() %d", len(escalationReasonFromPatient), len(EscalationReasons()))
	}
	for reason, want := range map[string]bool{
		EscalationReasonPatientAsked: true, EscalationReasonMaxRetries: true,
		EscalationReasonCupsPossibleMisread: false, EscalationReasonOCRNoCode: false,
		EscalationReasonOCRFailedReads: false, EscalationReasonQuantityOutOfRange: false,
		EscalationReasonSanitasBlock: false, EscalationReasonBotDisabled: false,
		"": true, "motivo_desconocido": true,
	} {
		if got := EscalationCountsTowardCap(reason); got != want {
			t.Errorf("EscalationCountsTowardCap(%q) = %v, quería %v", reason, got, want)
		}
	}
}
