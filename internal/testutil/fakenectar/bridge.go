// Package fakenectar es un puente de Néctar de prueba. Va aparte de testutil porque testutil importa
// repository/local, que importa notifications: desde los tests de notifications sería un ciclo.
package fakenectar

import (
	"context"
	"errors"
	"sync"

	"github.com/neuro-bot/neuro-bot/internal/bird"
)

// Bridge es un bird.NectarBridge de prueba para los sitios que mandan plantillas
// proactivas: la apertura sigue el guion (OpenID/OpenMsg o OpenErr), la ventana contesta Window /
// WindowErr, y todo lo que le llega queda anotado. Seguro para goroutines (los avisos de agenda
// envían en segundo plano).
type Bridge struct {
	mu sync.Mutex

	OpenID  int64
	OpenMsg string
	OpenErr error

	Window    bool
	WindowErr error

	// ContactValue / ContactErr son lo que contesta Contact.
	ContactValue bird.NectarContact
	ContactErr   error

	// CloseErr es lo que contesta Close; NotMineErr es el error que IsNotMine reconoce como
	// «ya no es mía» (nil = no reconoce ninguno).
	CloseErr   error
	NotMineErr error

	OpenPhones  []string // a qué teléfonos se intentó abrir
	TemplateIDs []int64  // a qué conversaciones se mandó una plantilla
	TextIDs     []int64  // a qué conversaciones se mandó un texto
	WindowAsks  []int64  // por qué conversaciones se preguntó la ventana
	CloseIDs    []int64  // qué conversaciones se cerraron
	ContactAsks []int64  // por qué conversaciones se preguntó el contacto

	// DocumentErr es lo que contesta SendDocument (nil = sale bien con el id «n-doc»).
	DocumentErr error
	Documents   []Document // los documentos enviados, en orden
}

// Document es un documento que se mandó por SendDocument.
type Document struct {
	ConversationID int64
	Filename       string
	Data           []byte
	Caption        string
}

var _ bird.NectarBridge = (*Bridge)(nil)

// Opens devuelve una copia de los teléfonos a los que se intentó abrir.
func (f *Bridge) Opens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.OpenPhones...)
}

// Texts devuelve una copia de las conversaciones que recibieron un texto.
func (f *Bridge) Texts() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.TextIDs...)
}

// Closes devuelve una copia de las conversaciones que se cerraron.
func (f *Bridge) Closes() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.CloseIDs...)
}

// WindowQuestions devuelve una copia de las conversaciones por las que se preguntó la ventana.
func (f *Bridge) WindowQuestions() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.WindowAsks...)
}

// OpenWithTemplate anota el teléfono y sigue el guion de la apertura.
func (f *Bridge) OpenWithTemplate(_ context.Context, phone string, _ bird.TemplateConfig) (int64, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.OpenPhones = append(f.OpenPhones, phone)
	if f.OpenErr != nil {
		return 0, "", f.OpenErr
	}
	if f.OpenID == 0 {
		return 0, "", errors.New("Bridge: OpenWithTemplate sin guion")
	}
	return f.OpenID, f.OpenMsg, nil
}

// WindowOpen anota la conversación y contesta Window / WindowErr.
func (f *Bridge) WindowOpen(_ context.Context, id int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.WindowAsks = append(f.WindowAsks, id)
	return f.Window, f.WindowErr
}

// SendTemplate anota la conversación que recibe la plantilla.
func (f *Bridge) SendTemplate(_ context.Context, id int64, _ bird.TemplateConfig) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.TemplateIDs = append(f.TemplateIDs, id)
	return "n-tmpl", nil
}

// SendText anota la conversación que recibe el texto.
func (f *Bridge) SendText(_ context.Context, id int64, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.TextIDs = append(f.TextIDs, id)
	return "n-text", nil
}

// SendButtons no se usa en estas pruebas: contesta un id fijo.
func (f *Bridge) SendButtons(context.Context, int64, string, []bird.Button) (string, error) {
	return "n-btn", nil
}

// SendList no se usa en estas pruebas: contesta un id fijo.
func (f *Bridge) SendList(context.Context, int64, string, string, []bird.ListSection) (string, error) {
	return "n-list", nil
}

// Available dice siempre que no se puede escalar.
func (f *Bridge) Available(context.Context, int64) (bool, string, error) {
	return false, "", nil
}

// Escalate no hace nada.
func (f *Bridge) Escalate(context.Context, int64, bird.Escalation) error { return nil }

// Close anota la conversación y contesta CloseErr.
func (f *Bridge) Close(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CloseIDs = append(f.CloseIDs, id)
	return f.CloseErr
}

// ConversationState contesta un estado vacío.
func (f *Bridge) ConversationState(context.Context, int64) (string, error) {
	return "", nil
}

// IsNotMine reconoce como «ya no es mía» solo NotMineErr.
func (f *Bridge) IsNotMine(err error) bool {
	return f.NotMineErr != nil && errors.Is(err, f.NotMineErr)
}

// SendDocument anota el documento enviado y contesta DocumentErr.
func (f *Bridge) SendDocument(_ context.Context, id int64, filename string, data []byte, caption string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Documents = append(f.Documents, Document{ConversationID: id, Filename: filename, Data: append([]byte(nil), data...), Caption: caption})
	if f.DocumentErr != nil {
		return "", f.DocumentErr
	}
	return "n-doc", nil
}

// SentDocuments devuelve una copia de los documentos enviados.
func (f *Bridge) SentDocuments() []Document {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Document(nil), f.Documents...)
}

// Contact anota la conversación y contesta ContactValue / ContactErr.
func (f *Bridge) Contact(_ context.Context, id int64) (bird.NectarContact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ContactAsks = append(f.ContactAsks, id)
	return f.ContactValue, f.ContactErr
}

// ContactQuestions devuelve una copia de las conversaciones por las que se preguntó el contacto.
func (f *Bridge) ContactQuestions() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.ContactAsks...)
}
