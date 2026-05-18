package whatsapp_cloud

import (
	"context"
	"fmt"
	"time"

	"github.com/madmike/go-infra/telemetry"
	"github.com/madmike/go-messengers/core"
)

// Send implements core.Messenger.
//
// WA Cloud API takes a single "type" per call. First attachment drives the
// primary message type; subsequent attachments are follow-up calls.
func (p *Protocol) Send(ctx context.Context, msg core.OutgoingMessage) (*core.SentMessage, error) {
	if msg.ChatID == "" {
		return nil, fmt.Errorf("whatsapp_cloud: chat_id (recipient phone) required")
	}
	if msg.Action != "" {
		// WhatsApp Cloud API doesn't support typing indicators.
		// We ignore it to avoid breaking the dispatcher.
		return nil, nil
	}
	if len(msg.Attachments) == 0 {
		return p.sendText(ctx, msg)
	}
	first, err := p.sendAttachment(ctx, msg, msg.Attachments[0])
	if err != nil {
		return nil, err
	}
	for _, att := range msg.Attachments[1:] {
		followUp := core.OutgoingMessage{ChatID: msg.ChatID, Attachments: []core.Attachment{att}}
		if _, err := p.sendAttachment(ctx, followUp, att); err != nil {
			p.logger.Warn("whatsapp_cloud: follow-up attachment failed", telemetry.Err(err))
		}
	}
	return first, nil
}

func (p *Protocol) sendText(ctx context.Context, msg core.OutgoingMessage) (*core.SentMessage, error) {
	payload := baseEnvelope(msg)
	payload["type"] = "text"
	payload["text"] = map[string]any{
		"body":        msg.Text,
		"preview_url": !msg.DisablePreview,
	}
	return p.postMessage(ctx, payload)
}

func (p *Protocol) sendAttachment(ctx context.Context, msg core.OutgoingMessage, att core.Attachment) (*core.SentMessage, error) {
	switch att.Type {
	case core.AttachmentLocation:
		if att.Location == nil {
			return nil, fmt.Errorf("whatsapp_cloud: location attachment missing payload")
		}
		payload := baseEnvelope(msg)
		payload["type"] = "location"
		payload["location"] = map[string]any{
			"latitude":  att.Location.Latitude,
			"longitude": att.Location.Longitude,
			"name":      att.Location.Title,
			"address":   att.Location.Address,
		}
		return p.postMessage(ctx, payload)

	case core.AttachmentContact:
		if att.Contact == nil {
			return nil, fmt.Errorf("whatsapp_cloud: contact attachment missing payload")
		}
		payload := baseEnvelope(msg)
		payload["type"] = "contacts"
		payload["contacts"] = []map[string]any{{
			"name": map[string]any{"formatted_name": att.Contact.Name, "first_name": att.Contact.Name},
			"phones": []map[string]any{{
				"phone": att.Contact.PhoneNumber,
				"type":  "CELL",
			}},
		}}
		return p.postMessage(ctx, payload)
	}

	kind := mediaKind(att.Type)
	if kind == "" {
		return nil, fmt.Errorf("whatsapp_cloud: unsupported attachment type %q", att.Type)
	}

	// Upload inline bytes first if no media_id present.
	mediaID := ""
	mediaLink := ""
	if att.FileRef != nil {
		mediaID = att.FileRef.ID
		mediaLink = att.FileRef.URL
	}
	if mediaID == "" && mediaLink == "" {
		if len(att.InlineData) == 0 {
			return nil, fmt.Errorf("whatsapp_cloud: attachment requires FileRef or InlineData")
		}
		ref, err := p.UploadFile(ctx, core.FileUpload{
			Data:     att.InlineData,
			MimeType: att.MimeType,
			FileName: att.FileName,
		})
		if err != nil {
			return nil, err
		}
		mediaID = ref.ID
	}

	mediaObj := map[string]any{}
	if mediaID != "" {
		mediaObj["id"] = mediaID
	} else {
		mediaObj["link"] = mediaLink
	}
	if att.Caption != "" {
		// Only image/video/document support captions.
		if kind == "image" || kind == "video" || kind == "document" {
			mediaObj["caption"] = att.Caption
		}
	}
	if att.FileName != "" && kind == "document" {
		mediaObj["filename"] = att.FileName
	}

	payload := baseEnvelope(msg)
	payload["type"] = kind
	payload[kind] = mediaObj
	return p.postMessage(ctx, payload)
}

// Edit is not supported on WA Cloud API (outside of template-level edits).
func (p *Protocol) Edit(ctx context.Context, chatID, messageID, newText string) (*core.SentMessage, error) {
	return nil, fmt.Errorf("whatsapp_cloud: message edits are not supported by the Cloud API")
}

// Delete is not supported either; WA only offers "mark as read".
func (p *Protocol) Delete(ctx context.Context, chatID, messageID string) error {
	return fmt.Errorf("whatsapp_cloud: message deletion is not supported by the Cloud API")
}

// MarkRead sends a read receipt (useful, exposed as a provider extension —
// services can type-assert to *Protocol to reach it).
func (p *Protocol) MarkRead(ctx context.Context, messageID string) error {
	payload := map[string]any{
		"messaging_product": "whatsapp",
		"status":            "read",
		"message_id":        messageID,
	}
	path := fmt.Sprintf("%s/messages", p.phoneNumberID)
	return p.graphCall(ctx, path, payload, nil)
}

// --- helpers ---

func (p *Protocol) postMessage(ctx context.Context, payload map[string]any) (*core.SentMessage, error) {
	var resp struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
		Contacts []struct {
			WAID string `json:"wa_id"`
		} `json:"contacts"`
	}
	path := fmt.Sprintf("%s/messages", p.phoneNumberID)
	if err := p.graphCall(ctx, path, payload, &resp); err != nil {
		return nil, err
	}
	sent := &core.SentMessage{SentAt: time.Now()}
	if len(resp.Messages) > 0 {
		sent.MessageID = resp.Messages[0].ID
	}
	if len(resp.Contacts) > 0 {
		sent.ChatID = resp.Contacts[0].WAID
	}
	return sent, nil
}

func baseEnvelope(msg core.OutgoingMessage) map[string]any {
	env := map[string]any{
		"messaging_product": "whatsapp",
		"recipient_type":    "individual",
		"to":                msg.ChatID,
	}
	if msg.ReplyTo != "" {
		env["context"] = map[string]any{"message_id": msg.ReplyTo}
	}
	return env
}

func mediaKind(t core.AttachmentType) string {
	switch t {
	case core.AttachmentImage:
		return "image"
	case core.AttachmentVideo:
		return "video"
	case core.AttachmentAudio:
		return "audio"
	case core.AttachmentVoice:
		return "audio" // WA conflates voice into audio
	case core.AttachmentDocument:
		return "document"
	case core.AttachmentSticker:
		return "sticker"
	default:
		return ""
	}
}
