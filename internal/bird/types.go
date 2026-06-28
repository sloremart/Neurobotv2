package bird

import "time"

// === Inbound Webhook ===

type WebhookEvent struct {
	Service string         `json:"service"`
	Event   string         `json:"event"`
	Payload WebhookPayload `json:"payload"`
}

type WebhookPayload struct {
	ID             string       `json:"id"`
	ChannelID      string       `json:"channelId"`
	Sender         SenderInfo   `json:"sender"`
	Receiver       ReceiverInfo `json:"receiver"`
	Body           MessageBody  `json:"body"`
	Status         string       `json:"status"`
	Direction      string       `json:"direction"`
	CreatedAt      string       `json:"createdAt"`
	ConversationID string       `json:"conversationId"`
}

type SenderInfo struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"displayName"`
	Contacts    []Contact `json:"contacts"` // Legacy format
	Contact     Contact   `json:"contact"`  // Bird actual format (singular)
}

type ReceiverInfo struct {
	ID        string    `json:"id"`
	Contacts  []Contact `json:"contacts"`  // Legacy format
	Connector Contact   `json:"connector"` // Bird actual format
}

type Contact struct {
	ID              string            `json:"id"`
	IdentifierValue string            `json:"identifierValue"` // +573001234567
	IdentifierKey   string            `json:"identifierKey"`   // phonenumber
	PlatformType    string            `json:"platformType"`    // whatsapp
	Annotations     map[string]string `json:"annotations"`     // {"name": "Edgar A."}
}

type MessageBody struct {
	Type        string    `json:"type"` // text, image, document, file, list, interactive
	Text        TextBody  `json:"text"`
	List        TextBody  `json:"list"`        // Bird may send list responses under "list" key
	Interactive TextBody  `json:"interactive"` // Bird may send interactive responses under "interactive" key
	Image       MediaBody `json:"image"`       // Bird image: body.image.images[0].mediaUrl
	File        FileBody  `json:"file"`        // Bird file/document: body.file.files[0].mediaUrl
}

type MediaBody struct {
	Images []MediaItem `json:"images"`
}

type FileBody struct {
	Files []FileItem `json:"files"`
}

type MediaItem struct {
	MediaURL string `json:"mediaUrl"`
}

type FileItem struct {
	MediaURL    string `json:"mediaUrl"`
	ContentType string `json:"contentType"`
	Filename    string `json:"filename"`
}

type TextBody struct {
	Text    string   `json:"text"`
	Actions []Action `json:"actions"`
}

type Action struct {
	Type     string   `json:"type"` // postback, reply
	Postback Postback `json:"postback"`
}

type Postback struct {
	Text    string `json:"text"`
	Payload string `json:"payload"` // confirm, cancelar, cancel, menu_1, etc.
}

// === Outbound Messages ===

type Button struct {
	Text    string
	Payload string
}

type ListSection struct {
	Title string
	Rows  []ListRow
}

type ListRow struct {
	ID          string
	Title       string
	Description string
}

type TemplateParam struct {
	Type  string `json:"type"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

type TemplateConfig struct {
	ProjectID string
	VersionID string
	Locale    string
	Params    []TemplateParam
}

// === Conversation Webhook ===

// ConversationEvent represents a webhook from the Conversations service.
// Event types: conversation.created, conversation.updated, conversation.deleted
type ConversationEvent struct {
	Service string              `json:"service"`
	Event   string              `json:"event"`
	Payload ConversationPayload `json:"payload"`
}

type ConversationPayload struct {
	ID                   string        `json:"id"`
	ChannelID            string        `json:"channelId"`
	Status               string        `json:"status"`
	CreatedAt            string        `json:"createdAt"`
	FeaturedParticipants []Participant `json:"featuredParticipants"`
}

type Participant struct {
	IdentifierValue string  `json:"identifierValue"`
	IdentifierKey   string  `json:"identifierKey"`
	PlatformType    string  `json:"platformType"`
	Contact         Contact `json:"contact"` // Alternative nesting
}

// === Agent Assignment API ===

// AgentListResponse is the response from GET /workspaces/{wsid}/agents
type AgentListResponse struct {
	Results []AgentInfo `json:"results"`
}

type AgentInfo struct {
	ID                    string            `json:"id"`
	DisplayName           string            `json:"name"` // Bird expone el nombre del agente en "name" (no "displayName")
	Teams                 []AgentTeam       `json:"teams"`
	Availability          AgentAvailability `json:"availability"`
	RootItemAssignedCount int               `json:"rootItemAssignedCount"` // Current workload
}

type AgentTeam struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type AgentAvailability struct {
	Status   string `json:"status"`   // "active", "inactive"
	Activity string `json:"activity"` // "available", "busy", "away"
}

// === Parsed Inbound Message ===

type InboundMessage struct {
	ID              string
	Phone           string // +573001234567
	DisplayName     string
	MessageType     string // text, image, document, postback, audio, video, location, contact, sticker
	Text            string // Texto del mensaje o payload del postback
	ImageURL        string
	DocumentURL     string
	ConversationID  string
	ReceivedAt      time.Time
	IsPostback      bool
	PostbackPayload string
}
