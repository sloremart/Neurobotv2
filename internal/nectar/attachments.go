package nectar

import (
	"bytes"
	"context"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"time"

	"github.com/neuro-bot/neuro-bot/internal/bird"
)

// Errores de los adjuntos (contrato del lote 11). ErrWindowClosed e ErrIdempotencyReused son los de
// siempre (client.go): el envío de un adjunto contesta lo mismo que los demás tipos.
var (
	// ErrUnsupportedMedia es el 415 de la subida: Néctar no acepta ese tipo de archivo.
	ErrUnsupportedMedia = fmt.Errorf("nectar: tipo de archivo no soportado (415): %w", bird.ErrPermanentSend)
	// ErrTooLarge es el 413 de la subida: el archivo pasa el tope de su tipo (5 MiB imágenes, 16 MiB PDF).
	ErrTooLarge = fmt.Errorf("nectar: archivo demasiado grande (413): %w", bird.ErrPermanentSend)
	// ErrAttachmentUnavailable es el 422 attachment_unavailable del envío (reason expired|not_found):
	// el adjunto ya no existe en Néctar; hay que volver a subirlo.
	ErrAttachmentUnavailable = fmt.Errorf("nectar: adjunto no disponible (422): %w", bird.ErrPermanentSend)
)

// uploadRetryAfterMax es la mayor espera de un 503 con Retry-After que la subida respeta.
const uploadRetryAfterMax = 30 * time.Second

// Attachment es lo que Néctar contesta a una subida: el id que luego se envía y los datos que midió.
type Attachment struct {
	ID        string    `json:"attachment_id"`
	MIME      string    `json:"mime"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	Filename  string    `json:"filename"`
	ExpiresAt time.Time `json:"expires_at"`
}

// attachmentRef es el bloque `attachment` del envío: el id subido y un pie opcional.
type attachmentRef struct {
	ID      string `json:"id"`
	Caption string `json:"caption,omitempty"`
}

// Upload sube un archivo a POST /api/bot/uploads (multipart: idempotency_key, filename, file). Con la
// misma clave y el mismo contenido Néctar contesta lo mismo (200); con otro contenido, 409
// idempotency_key_reused. Un 503 con Retry-After se reintenta UNA vez tras esa espera, con la misma
// clave. El cuerpo se serializa antes y la firma cubre esos bytes exactos.
func (c *Client) Upload(ctx context.Context, idempotencyKey, filename string, data []byte) (Attachment, error) {
	body, contentType, err := multipartBody(idempotencyKey, filename, data)
	if err != nil {
		return Attachment{}, err
	}
	const path = "/api/bot/uploads"
	resp, err := c.exchangeRaw(ctx, http.MethodPost, path, body, contentType, nil)
	if err != nil {
		return Attachment{}, err
	}
	if resp.status == http.StatusServiceUnavailable {
		wait := parseRetryAfter(resp.header.Get("Retry-After"))
		if wait > 0 && wait <= uploadRetryAfterMax {
			if err := c.sleep(ctx, wait); err != nil {
				return Attachment{}, err
			}
			if resp, err = c.exchangeRaw(ctx, http.MethodPost, path, body, contentType, nil); err != nil {
				return Attachment{}, err
			}
		}
	}
	switch resp.status {
	case http.StatusNotFound:
		// La ruta de subida no tiene conversación: un 404 es Néctar sin el lote 11 o ruta equivocada,
		// no «ya no es mía» (ErrNotMine soltaría el caso).
		return Attachment{}, newStatusError(resp, "nectar: POST "+path+": status 404 (¿Néctar sin el lote 11?)")
	case http.StatusRequestEntityTooLarge:
		return Attachment{}, ErrTooLarge
	case http.StatusUnsupportedMediaType:
		return Attachment{}, ErrUnsupportedMedia
	}
	var out Attachment
	if err := interpretReply(resp, http.MethodPost, path, &out); err != nil {
		return Attachment{}, err
	}
	return out, nil
}

// multipartBody arma el cuerpo multipart de la subida y devuelve también su Content-Type (con el límite).
func multipartBody(idempotencyKey, filename string, data []byte) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("idempotency_key", idempotencyKey); err != nil {
		return nil, "", err
	}
	if err := w.WriteField("filename", filename); err != nil {
		return nil, "", err
	}
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": filename}))
	h.Set("Content-Type", http.DetectContentType(data))
	part, err := w.CreatePart(h)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(data); err != nil {
		return nil, "", err
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// SendAttachment envía un adjunto ya subido por la conversación (POST …/messages, type=attachment) y
// devuelve el id del mensaje. Usa la misma ruta de envío que el resto: idempotencia y claves en duda
// incluidas. Un caption vacío no viaja.
func (c *Client) SendAttachment(ctx context.Context, conversationID int64, attachmentID, caption string) (int64, error) {
	return c.send(ctx, conversationID, sendRequest{
		Type:       "attachment",
		Attachment: &attachmentRef{ID: attachmentID, Caption: caption},
	})
}
