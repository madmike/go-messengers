package whatsapp_cloud

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/madmike/go-infra/telemetry"
	"github.com/madmike/go-messengers/core"
)

// Listen registers a handler for webhook-delivered updates. WA Cloud API has
// no long-polling equivalent.
func (p *Protocol) Listen(ctx context.Context, handler core.UpdateHandler) error {
	if handler == nil {
		return fmt.Errorf("whatsapp_cloud: nil update handler")
	}
	p.mu.Lock()
	p.handler = handler
	p.mu.Unlock()
	return nil
}

// Poll is a no-op — WA Cloud API is webhook-only. Returns an error so callers
// that accidentally rely on polling semantics fail fast.
func (p *Protocol) Poll(ctx context.Context, handler core.UpdateHandler) error {
	return fmt.Errorf("whatsapp_cloud: long-polling not supported; use Listen + HTTPHandler")
}

// RegisterWebhook is a no-op because WA webhooks are configured in the Meta
// developer dashboard, not via API. Present so Protocol satisfies the same
// bootstrap shape as the Telegram protocol.
func (p *Protocol) RegisterWebhook(ctx context.Context) error {
	return nil
}

// --- webhook handler ---

type webhookHandler struct {
	protocol *Protocol
}

func (h *webhookHandler) Path() string { return "/" }

func (h *webhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	switch r.Method {
	case http.MethodGet:
		// Meta's verification handshake: echoes hub.challenge if verify_token matches.
		if r.URL.Query().Get("hub.mode") == "subscribe" &&
			r.URL.Query().Get("hub.verify_token") == h.protocol.verifyToken {
			w.Write([]byte(r.URL.Query().Get("hub.challenge")))
			return
		}
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	case http.MethodPost:
		raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if h.protocol.appSecret != "" {
			if !validateSignature(r.Header.Get("X-Hub-Signature-256"), raw, h.protocol.appSecret) {
				http.Error(w, "bad signature", http.StatusUnauthorized)
				return
			}
		}
		if err := h.dispatch(r.Context(), raw); err != nil {
			h.protocol.logger.Warn("whatsapp_cloud: webhook dispatch", telemetry.Err(err))
		}
		w.WriteHeader(http.StatusOK)
		return
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *webhookHandler) ServeHTTPRaw(ctx context.Context, req core.HTTPRequest) (core.HTTPResponse, error) {
	switch strings.ToUpper(req.Method) {
	case http.MethodGet:
		mode := firstQueryValue(req.Query, "hub.mode")
		token := firstQueryValue(req.Query, "hub.verify_token")
		challenge := firstQueryValue(req.Query, "hub.challenge")
		if mode == "subscribe" && token == h.protocol.verifyToken {
			return core.HTTPResponse{Status: http.StatusOK, Body: []byte(challenge)}, nil
		}
		return core.HTTPResponse{Status: http.StatusForbidden}, nil
	case http.MethodPost:
		if h.protocol.appSecret != "" {
			if !validateSignature(headerValue(req.Headers, "X-Hub-Signature-256"), req.Body, h.protocol.appSecret) {
				return core.HTTPResponse{Status: http.StatusUnauthorized}, nil
			}
		}
		if err := h.dispatch(ctx, req.Body); err != nil {
			return core.HTTPResponse{Status: http.StatusBadRequest, Body: []byte(err.Error())}, nil
		}
		return core.HTTPResponse{Status: http.StatusOK, Body: []byte(`{"ok":true}`)}, nil
	}
	return core.HTTPResponse{Status: http.StatusMethodNotAllowed}, nil
}

func (h *webhookHandler) dispatch(ctx context.Context, raw []byte) error {
	var wh whatsappWebhook
	if err := json.Unmarshal(raw, &wh); err != nil {
		return fmt.Errorf("whatsapp_cloud: decode webhook: %w", err)
	}
	h.protocol.mu.RLock()
	handler := h.protocol.handler
	h.protocol.mu.RUnlock()
	if handler == nil {
		return nil
	}
	for _, update := range wh.toUpdates() {
		go handler(ctx, update)
	}
	return nil
}

func validateSignature(header string, body []byte, secret string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	expected, err := hex.DecodeString(header[len(prefix):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(expected, mac.Sum(nil))
}

func headerValue(h map[string][]string, key string) string {
	for k, v := range h {
		if strings.EqualFold(k, key) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

func firstQueryValue(q map[string][]string, key string) string {
	if v, ok := q[key]; ok && len(v) > 0 {
		return v[0]
	}
	return ""
}

// --- webhook payload ---

type whatsappWebhook struct {
	Object string          `json:"object"`
	Entry  []whatsappEntry `json:"entry"`
}

type whatsappEntry struct {
	ID      string           `json:"id"`
	Changes []whatsappChange `json:"changes"`
}

type whatsappChange struct {
	Field string        `json:"field"`
	Value whatsappValue `json:"value"`
}

type whatsappValue struct {
	MessagingProduct string            `json:"messaging_product"`
	Metadata         whatsappMetadata  `json:"metadata"`
	Contacts         []whatsappContact `json:"contacts"`
	Messages         []whatsappMessage `json:"messages"`
	Statuses         []whatsappStatus  `json:"statuses"`
}

type whatsappMetadata struct {
	DisplayPhoneNumber string `json:"display_phone_number"`
	PhoneNumberID      string `json:"phone_number_id"`
}

type whatsappContact struct {
	WAID    string `json:"wa_id"`
	Profile struct {
		Name string `json:"name"`
	} `json:"profile"`
}

type whatsappMessage struct {
	From        string                   `json:"from"`
	ID          string                   `json:"id"`
	Timestamp   string                   `json:"timestamp"`
	Type        string                   `json:"type"`
	Text        *whatsappText            `json:"text"`
	Image       *whatsappMedia           `json:"image"`
	Video       *whatsappMedia           `json:"video"`
	Audio       *whatsappMedia           `json:"audio"`
	Voice       *whatsappMedia           `json:"voice"`
	Document    *whatsappMedia           `json:"document"`
	Sticker     *whatsappMedia           `json:"sticker"`
	Location    *whatsappLocation        `json:"location"`
	Contacts    []whatsappContactPayload `json:"contacts"`
	Button      *whatsappButtonReply     `json:"button"`
	Interactive *whatsappInteractive     `json:"interactive"`
	Reaction    *whatsappReaction        `json:"reaction"`
	Context     *struct {
		ID string `json:"id"`
	} `json:"context"`
}

type whatsappText struct {
	Body string `json:"body"`
}

type whatsappMedia struct {
	ID       string `json:"id"`
	MimeType string `json:"mime_type"`
	Sha256   string `json:"sha256"`
	Caption  string `json:"caption"`
	Filename string `json:"filename"`
}

type whatsappLocation struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Name      string  `json:"name"`
	Address   string  `json:"address"`
}

type whatsappContactPayload struct {
	Name struct {
		FormattedName string `json:"formatted_name"`
	} `json:"name"`
	Phones []struct {
		Phone string `json:"phone"`
		WAID  string `json:"wa_id"`
	} `json:"phones"`
}

type whatsappButtonReply struct {
	Text    string `json:"text"`
	Payload string `json:"payload"`
}

type whatsappInteractive struct {
	Type        string `json:"type"`
	ButtonReply *struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"button_reply"`
	ListReply *struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"list_reply"`
}

type whatsappReaction struct {
	MessageID string `json:"message_id"`
	Emoji     string `json:"emoji"`
}

type whatsappStatus struct {
	ID          string `json:"id"`
	Status      string `json:"status"` // sent / delivered / read / failed
	Timestamp   string `json:"timestamp"`
	RecipientID string `json:"recipient_id"`
}

func (w whatsappWebhook) toUpdates() []core.Update {
	var out []core.Update
	for _, entry := range w.Entry {
		for _, change := range entry.Changes {
			for _, m := range change.Value.Messages {
				out = append(out, m.toUpdate(change.Value))
			}
			// statuses and error-only events collapse to UpdateTypeUnknown today.
		}
	}
	return out
}

func (m whatsappMessage) toUpdate(val whatsappValue) core.Update {
	chat := core.Chat{
		ID:       m.From,
		Platform: core.PlatformWhatsApp,
		Type:     core.ChatTypePrivate,
	}
	var fromName string
	for _, c := range val.Contacts {
		if c.WAID == m.From {
			fromName = c.Profile.Name
			break
		}
	}
	from := core.Account{
		ID:          m.From,
		Platform:    core.PlatformWhatsApp,
		AccountType: core.AccountTypeBusinessAPI,
		Phone:       m.From,
		DisplayName: fromName,
	}
	update := core.Update{
		UpdateID:   m.ID,
		Platform:   core.PlatformWhatsApp,
		ReceivedAt: time.Now(),
		Chat:       chat,
		From:       from,
	}

	switch m.Type {
	case "reaction":
		update.Type = core.UpdateTypeReaction
		if m.Reaction != nil {
			update.Reaction = &core.Reaction{
				MessageID: m.Reaction.MessageID,
				Emoji:     m.Reaction.Emoji,
				Added:     m.Reaction.Emoji != "",
			}
		}
	case "interactive", "button":
		update.Type = core.UpdateTypeCallback
		cb := &core.CallbackQuery{}
		if m.Button != nil {
			cb.Data = m.Button.Payload
		}
		if m.Interactive != nil {
			if m.Interactive.ButtonReply != nil {
				cb.Data = m.Interactive.ButtonReply.ID
			} else if m.Interactive.ListReply != nil {
				cb.Data = m.Interactive.ListReply.ID
			}
		}
		cb.ID = m.ID
		if m.Context != nil {
			cb.MessageID = m.Context.ID
		}
		update.Callback = cb
	default:
		update.Type = core.UpdateTypeMessage
		update.Message = m.toIncoming()
	}
	return update
}

func (m whatsappMessage) toIncoming() *core.IncomingMessage {
	inc := &core.IncomingMessage{
		MessageID: m.ID,
		SentAt:    parseUnixString(m.Timestamp),
	}
	if m.Context != nil {
		inc.ReplyTo = m.Context.ID
	}
	if m.Text != nil {
		inc.Text = m.Text.Body
	}
	addMedia := func(t core.AttachmentType, media *whatsappMedia) {
		if media == nil {
			return
		}
		if inc.Text == "" && media.Caption != "" {
			inc.Text = media.Caption
		}
		inc.Attachments = append(inc.Attachments, core.Attachment{
			Type:     t,
			MimeType: media.MimeType,
			FileName: media.Filename,
			Caption:  media.Caption,
			FileRef: &core.FileRef{
				Platform: core.PlatformWhatsApp,
				ID:       media.ID,
				MimeType: media.MimeType,
			},
		})
	}
	addMedia(core.AttachmentImage, m.Image)
	addMedia(core.AttachmentVideo, m.Video)
	addMedia(core.AttachmentAudio, m.Audio)
	addMedia(core.AttachmentVoice, m.Voice)
	addMedia(core.AttachmentDocument, m.Document)
	addMedia(core.AttachmentSticker, m.Sticker)
	if m.Location != nil {
		inc.Attachments = append(inc.Attachments, core.Attachment{
			Type: core.AttachmentLocation,
			Location: &core.Location{
				Latitude:  m.Location.Latitude,
				Longitude: m.Location.Longitude,
				Title:     m.Location.Name,
				Address:   m.Location.Address,
			},
		})
	}
	for _, c := range m.Contacts {
		phone := ""
		userID := ""
		if len(c.Phones) > 0 {
			phone = c.Phones[0].Phone
			userID = c.Phones[0].WAID
		}
		inc.Attachments = append(inc.Attachments, core.Attachment{
			Type: core.AttachmentContact,
			Contact: &core.Contact{
				Name:        c.Name.FormattedName,
				PhoneNumber: phone,
				UserID:      userID,
			},
		})
	}
	return inc
}

func parseUnixString(s string) time.Time {
	if s == "" {
		return time.Now()
	}
	var secs int64
	fmt.Sscanf(s, "%d", &secs)
	if secs == 0 {
		return time.Now()
	}
	return time.Unix(secs, 0)
}
