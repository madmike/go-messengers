package telegram_bot

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/madmike/go-infra/telemetry"
	"github.com/madmike/go-messengers/core"
)

// Send implements core.Messenger.
//
// Dispatch policy:
//   - Plain text → sendMessage
//   - First image / video / audio / document / voice attachment →
//     send<Photo|Video|Audio|Document|Voice>. Remaining attachments are
//     sent as follow-up messages (Telegram has a sendMediaGroup endpoint
//     we could upgrade to later).
//   - Location / Contact → sendLocation / sendContact.
func (p *Protocol) Send(ctx context.Context, msg core.OutgoingMessage) (*core.SentMessage, error) {
	if msg.ChatID == "" {
		return nil, fmt.Errorf("telegram_bot: chat_id required")
	}

	if msg.Action != "" {
		if err := p.sendChatAction(ctx, msg.ChatID, msg.Action, msg.ThreadID); err != nil {
			return nil, fmt.Errorf("send chat action: %w", err)
		}
		return &core.SentMessage{ChatID: msg.ChatID}, nil
	}

	// No attachments → plain text.
	if len(msg.Attachments) == 0 {
		return p.sendText(ctx, msg)
	}

	// First attachment drives the outgoing call; rest become follow-ups.
	first, err := p.sendAttachment(ctx, msg, msg.Attachments[0])
	if err != nil {
		return nil, err
	}
	for _, att := range msg.Attachments[1:] {
		followUp := core.OutgoingMessage{ChatID: msg.ChatID, ThreadID: msg.ThreadID, Attachments: []core.Attachment{att}}
		if _, err := p.sendAttachment(ctx, followUp, att); err != nil {
			p.logger.Warn("telegram_bot: follow-up attachment failed", telemetry.Err(err))
		}
	}
	return first, nil
}

func (p *Protocol) sendText(ctx context.Context, msg core.OutgoingMessage) (*core.SentMessage, error) {
	body := map[string]any{
		"chat_id": msg.ChatID,
		"text":    msg.Text,
	}
	applyCommonFields(body, msg)
	var resp sendMessageResponse
	if err := p.apiCall(ctx, "sendMessage", body, &resp); err != nil {
		return nil, err
	}
	return resp.toSentMessage(), nil
}

func (p *Protocol) sendAttachment(ctx context.Context, msg core.OutgoingMessage, att core.Attachment) (*core.SentMessage, error) {
	switch att.Type {
	case core.AttachmentLocation:
		if att.Location == nil {
			return nil, fmt.Errorf("telegram_bot: location attachment missing payload")
		}
		body := map[string]any{
			"chat_id":   msg.ChatID,
			"latitude":  att.Location.Latitude,
			"longitude": att.Location.Longitude,
		}
		applyCommonFields(body, msg)
		var resp sendMessageResponse
		if err := p.apiCall(ctx, "sendLocation", body, &resp); err != nil {
			return nil, err
		}
		return resp.toSentMessage(), nil

	case core.AttachmentContact:
		if att.Contact == nil {
			return nil, fmt.Errorf("telegram_bot: contact attachment missing payload")
		}
		body := map[string]any{
			"chat_id":      msg.ChatID,
			"phone_number": att.Contact.PhoneNumber,
			"first_name":   att.Contact.Name,
		}
		applyCommonFields(body, msg)
		var resp sendMessageResponse
		if err := p.apiCall(ctx, "sendContact", body, &resp); err != nil {
			return nil, err
		}
		return resp.toSentMessage(), nil
	}

	method, fileField := methodForAttachment(att.Type)
	if method == "" {
		return nil, fmt.Errorf("telegram_bot: unsupported attachment type %q", att.Type)
	}

	// Reuse already-uploaded file by ID → JSON POST is enough.
	if att.FileRef != nil && att.FileRef.ID != "" {
		body := map[string]any{
			"chat_id": msg.ChatID,
			fileField: att.FileRef.ID,
			"caption": firstNonEmpty(att.Caption, msg.Text),
		}
		applyCommonFields(body, msg)
		var resp sendMessageResponse
		if err := p.apiCall(ctx, method, body, &resp); err != nil {
			return nil, err
		}
		return resp.toSentMessage(), nil
	}

	// Inline bytes → multipart upload.
	if len(att.InlineData) == 0 {
		return nil, fmt.Errorf("telegram_bot: attachment requires FileRef.ID or InlineData")
	}
	fields := map[string]string{
		"chat_id": msg.ChatID,
		"caption": firstNonEmpty(att.Caption, msg.Text),
	}
	if msg.ThreadID != "" {
		fields["message_thread_id"] = msg.ThreadID
	}
	if msg.ReplyTo != "" {
		fields["reply_to_message_id"] = msg.ReplyTo
	}
	fileName := att.FileName
	if fileName == "" {
		fileName = defaultFilenameFor(att.Type)
	}
	mp, err := newMultipart(fields, &multipartFile{
		field:    fileField,
		filename: fileName,
		data:     att.InlineData,
	})
	if err != nil {
		return nil, err
	}
	var resp sendMessageResponse
	if err := p.apiCall(ctx, method, mp, &resp); err != nil {
		return nil, err
	}
	return resp.toSentMessage(), nil
}

// Edit implements core.Messenger.
func (p *Protocol) Edit(ctx context.Context, chatID, messageID, newText string) (*core.SentMessage, error) {
	body := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       newText,
	}
	var resp sendMessageResponse
	if err := p.apiCall(ctx, "editMessageText", body, &resp); err != nil {
		return nil, err
	}
	return resp.toSentMessage(), nil
}

// Delete implements core.Messenger.
func (p *Protocol) Delete(ctx context.Context, chatID, messageID string) error {
	body := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
	}
	return p.apiCall(ctx, "deleteMessage", body, nil)
}

func (p *Protocol) sendChatAction(ctx context.Context, chatID string, action string, threadID string) error {
	body := map[string]any{
		"chat_id": chatID,
		"action":  action,
	}
	if threadID != "" {
		body["message_thread_id"] = threadID
	}
	return p.apiCall(ctx, "sendChatAction", body, nil)
}

// --- helpers ---

func applyCommonFields(body map[string]any, msg core.OutgoingMessage) {
	if msg.ThreadID != "" {
		body["message_thread_id"] = msg.ThreadID
	}
	if msg.ReplyTo != "" {
		body["reply_to_message_id"] = msg.ReplyTo
	}
	if mode := telegramParseMode(msg.ParseMode); mode != "" {
		body["parse_mode"] = mode
	}
	if msg.DisablePreview {
		body["disable_web_page_preview"] = true
	}
	if msg.DisableNotification || msg.Silent {
		body["disable_notification"] = true
	}
	if kb := telegramKeyboard(msg.Buttons); kb != nil {
		body["reply_markup"] = kb
	}
}

func telegramParseMode(pm core.ParseMode) string {
	switch pm {
	case core.ParseModeMarkdown:
		return "Markdown"
	case core.ParseModeMarkdownV2:
		return "MarkdownV2"
	case core.ParseModeHTML:
		return "HTML"
	default:
		return ""
	}
}

func telegramKeyboard(rows [][]core.Button) any {
	if len(rows) == 0 {
		return nil
	}
	out := make([][]map[string]any, 0, len(rows))
	for _, row := range rows {
		cells := make([]map[string]any, 0, len(row))
		for _, b := range row {
			cell := map[string]any{"text": b.Text}
			switch {
			case b.CallbackData != "":
				cell["callback_data"] = b.CallbackData
			case b.URL != "":
				cell["url"] = b.URL
			case b.SwitchInline != "":
				cell["switch_inline_query"] = b.SwitchInline
			}
			cells = append(cells, cell)
		}
		out = append(out, cells)
	}
	return map[string]any{"inline_keyboard": out}
}

func methodForAttachment(t core.AttachmentType) (method, fileField string) {
	switch t {
	case core.AttachmentImage:
		return "sendPhoto", "photo"
	case core.AttachmentVideo:
		return "sendVideo", "video"
	case core.AttachmentAudio:
		return "sendAudio", "audio"
	case core.AttachmentVoice:
		return "sendVoice", "voice"
	case core.AttachmentDocument:
		return "sendDocument", "document"
	case core.AttachmentSticker:
		return "sendSticker", "sticker"
	default:
		return "", ""
	}
}

func defaultFilenameFor(t core.AttachmentType) string {
	switch t {
	case core.AttachmentImage:
		return "image.jpg"
	case core.AttachmentVideo:
		return "video.mp4"
	case core.AttachmentAudio:
		return "audio.mp3"
	case core.AttachmentVoice:
		return "voice.ogg"
	case core.AttachmentDocument:
		return "file.bin"
	case core.AttachmentSticker:
		return "sticker.webp"
	default:
		return "file.bin"
	}
}

// sendMessageResponse captures the fields we need from Telegram's Message
// object returned by send* endpoints.
type sendMessageResponse struct {
	MessageID int `json:"message_id"`
	Chat      struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	Date int64 `json:"date"`

	// Keep the full payload so callers can inspect provider-specific fields.
	Raw json.RawMessage `json:"-"`
}

func (r sendMessageResponse) toSentMessage() *core.SentMessage {
	return &core.SentMessage{
		MessageID: fmt.Sprintf("%d", r.MessageID),
		ChatID:    fmt.Sprintf("%d", r.Chat.ID),
		SentAt:    time.Unix(r.Date, 0),
	}
}
