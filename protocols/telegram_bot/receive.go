package telegram_bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/madmike/go-infra/telemetry"
	"github.com/madmike/go-messengers/core"
)

// pollConflictThreshold is the number of consecutive 409 conflicts the Telegram
// long-poll will tolerate before signalling OnPollConflict so the surrounding
// registry can reconcile against the source of truth.
const pollConflictThreshold = 3

// Listen registers an UpdateHandler for webhook-delivered updates.
// A POST to the registered webhook URL from Telegram is translated into a
// core.Update and dispatched to the handler.
//
// Listen does NOT call setWebhook for you — services that know their public
// URL should call RegisterWebhook explicitly during bootstrap.
func (p *Protocol) Listen(ctx context.Context, handler core.UpdateHandler) error {
	if handler == nil {
		return fmt.Errorf("telegram_bot: nil update handler")
	}
	p.mu.Lock()
	p.handler = handler
	p.mu.Unlock()
	return nil
}

// Poll runs long-polling against getUpdates until ctx is cancelled. Useful
// for services that can't expose a public webhook (local dev, test envs).
func (p *Protocol) Poll(ctx context.Context, handler core.UpdateHandler) error {
	if handler == nil {
		return fmt.Errorf("telegram_bot: nil update handler")
	}
	p.mu.Lock()
	if p.pollCancel != nil {
		p.pollCancel()
		done := p.pollDone
		p.mu.Unlock()
		if done != nil {
			<-done
		}
		p.mu.Lock()
	}

	pollCtx, cancel := context.WithCancel(ctx)
	p.pollCancel = cancel
	p.pollDone = make(chan struct{})
	p.handler = handler
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		if p.pollDone != nil {
			close(p.pollDone)
			p.pollDone = nil
		}
		p.pollCancel = nil
		p.mu.Unlock()
	}()

	// Ensure webhook is deleted before polling, otherwise getUpdates will fail with 409 Conflict
	if err := p.apiCall(pollCtx, "deleteWebhook", map[string]any{"drop_pending_updates": false}, nil); err != nil {
		p.logger.Warn("telegram_bot: deleteWebhook failed", telemetry.Err(err))
	}

	var offset int64
	var conflictCount int
	var conflictFired int32 // atomic 0/1 — fire the callback only once per poll session
	backoff := 2 * time.Second
	const maxBackoff = 60 * time.Second
	for {
		select {
		case <-pollCtx.Done():
			return pollCtx.Err()
		default:
		}
		body := map[string]any{
			"timeout": 25,
			"offset":  offset,
		}
		var updates []telegramUpdate
		if err := p.apiCall(pollCtx, "getUpdates", body, &updates); err != nil {
			if pollCtx.Err() != nil {
				return pollCtx.Err()
			}
			isConflict := isPollConflict(err)
			if isConflict {
				conflictCount++
				p.logger.Warn("telegram_bot: getUpdates conflict; retrying",
					telemetry.Err(err), telemetry.Int("consecutive_conflicts", conflictCount))
				if conflictCount >= pollConflictThreshold && p.onPollConflict != nil &&
					atomic.CompareAndSwapInt32(&conflictFired, 0, 1) {
					p.logger.Warn("telegram_bot: persistent poll conflict, requesting reconcile",
						telemetry.Int("consecutive_conflicts", conflictCount))
					go p.onPollConflict()
				}
			} else {
				conflictCount = 0
				p.logger.Warn("telegram_bot: getUpdates failed; retrying", telemetry.Err(err))
			}
			sleepCtx(pollCtx, backoff)
			if backoff < maxBackoff {
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
			continue
		}
		// success → reset failure state
		conflictCount = 0
		backoff = 2 * time.Second
		for _, u := range updates {
			if int64(u.UpdateID) >= offset {
				offset = int64(u.UpdateID) + 1
			}
			normalized := u.toUpdate()
			go handler(pollCtx, normalized)
		}
	}
}

// isPollConflict reports whether the error from apiCall is the Telegram
// getUpdates "409 Conflict: terminated by other getUpdates request" response.
func isPollConflict(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "failed (409)")
}

// sleepCtx sleeps for d or returns early when ctx is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// RegisterWebhook calls setWebhook on the Bot API. Safe to call multiple times.
func (p *Protocol) RegisterWebhook(ctx context.Context) error {
	if p.webhookURL == "" {
		return fmt.Errorf("telegram_bot: PublicWebhookURL not configured")
	}
	body := map[string]any{
		"url":             p.webhookURL,
		"allowed_updates": []string{"message", "edited_message", "callback_query", "message_reaction", "chat_member"},
	}
	if p.secretTok != "" {
		body["secret_token"] = p.secretTok
	}
	return p.apiCall(ctx, "setWebhook", body, nil)
}

// --- webhook handler ---

type webhookHandler struct {
	protocol *Protocol
}

func (h *webhookHandler) Path() string { return "/" }

func (h *webhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	if r.Method != http.MethodPost {
		http.Error(w, "expected POST", http.StatusMethodNotAllowed)
		return
	}
	// Secret-token check: Telegram includes the configured secret as header.
	if h.protocol.secretTok != "" {
		got := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
		if got != h.protocol.secretTok {
			http.Error(w, "bad secret", http.StatusUnauthorized)
			return
		}
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.dispatch(r.Context(), raw); err != nil {
		h.protocol.logger.Warn("telegram_bot: webhook dispatch", telemetry.Err(err))
	}
	w.WriteHeader(http.StatusOK)
}

func (h *webhookHandler) ServeHTTPRaw(ctx context.Context, req core.HTTPRequest) (core.HTTPResponse, error) {
	if !strings.EqualFold(req.Method, http.MethodPost) {
		return core.HTTPResponse{Status: http.StatusMethodNotAllowed}, nil
	}
	if h.protocol.secretTok != "" {
		if secret := headerValue(req.Headers, "X-Telegram-Bot-Api-Secret-Token"); secret != h.protocol.secretTok {
			return core.HTTPResponse{Status: http.StatusUnauthorized}, nil
		}
	}
	if err := h.dispatch(ctx, req.Body); err != nil {
		return core.HTTPResponse{Status: http.StatusBadRequest, Body: []byte(err.Error())}, nil
	}
	return core.HTTPResponse{Status: http.StatusOK, Body: []byte(`{"ok":true}`)}, nil
}

func (h *webhookHandler) dispatch(ctx context.Context, raw []byte) error {
	var u telegramUpdate
	if err := json.Unmarshal(raw, &u); err != nil {
		return fmt.Errorf("telegram_bot: decode update: %w", err)
	}
	h.protocol.mu.RLock()
	handler := h.protocol.handler
	h.protocol.mu.RUnlock()
	if handler == nil {
		return nil
	}
	go handler(ctx, u.toUpdate())
	return nil
}

func headerValue(h map[string][]string, key string) string {
	for k, v := range h {
		if strings.EqualFold(k, key) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// --- update decoding ---

type telegramUpdate struct {
	UpdateID        int                 `json:"update_id"`
	Message         *telegramMessage    `json:"message"`
	EditedMessage   *telegramMessage    `json:"edited_message"`
	CallbackQuery   *telegramCallback   `json:"callback_query"`
	MessageReaction *telegramReaction   `json:"message_reaction"`
	ChatMember      *telegramChatMember `json:"chat_member"`
}

type telegramMessage struct {
	MessageID       int                 `json:"message_id"`
	Date            int64               `json:"date"`
	Chat            telegramChat        `json:"chat"`
	From            *telegramUser       `json:"from"`
	Text            string              `json:"text"`
	Caption         string              `json:"caption"`
	ReplyToMessage  *telegramMessage    `json:"reply_to_message"`
	MessageThreadID *int                `json:"message_thread_id"`
	Photo           []telegramPhotoSize `json:"photo"`
	Document        *telegramDocument   `json:"document"`
	Voice           *telegramVoice      `json:"voice"`
	Audio           *telegramAudio      `json:"audio"`
	Video           *telegramVideo      `json:"video"`
	Location        *telegramLocation   `json:"location"`
	Contact         *telegramContact    `json:"contact"`
}

type telegramChat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Username string `json:"username"`
	IsForum  bool   `json:"is_forum"`
}

type telegramUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type telegramPhotoSize struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FileSize int64  `json:"file_size"`
}

type telegramDocument struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

type telegramVoice struct {
	FileID   string `json:"file_id"`
	Duration int    `json:"duration"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

type telegramAudio struct {
	FileID   string `json:"file_id"`
	Duration int    `json:"duration"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
	FileName string `json:"file_name"`
}

type telegramVideo struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Duration int    `json:"duration"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

type telegramLocation struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type telegramContact struct {
	PhoneNumber string `json:"phone_number"`
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name"`
	UserID      int64  `json:"user_id"`
}

type telegramCallback struct {
	ID      string           `json:"id"`
	From    telegramUser     `json:"from"`
	Message *telegramMessage `json:"message"`
	Data    string           `json:"data"`
}

type telegramReaction struct {
	MessageID   int          `json:"message_id"`
	Chat        telegramChat `json:"chat"`
	User        telegramUser `json:"user"`
	NewReaction []struct {
		Emoji string `json:"emoji"`
	} `json:"new_reaction"`
	OldReaction []struct {
		Emoji string `json:"emoji"`
	} `json:"old_reaction"`
}

type telegramChatMember struct {
	Chat          telegramChat `json:"chat"`
	From          telegramUser `json:"from"`
	OldChatMember struct {
		Status string `json:"status"`
	} `json:"old_chat_member"`
	NewChatMember struct {
		Status string       `json:"status"`
		User   telegramUser `json:"user"`
	} `json:"new_chat_member"`
}

func (u telegramUpdate) toUpdate() core.Update {
	out := core.Update{
		UpdateID:   fmt.Sprintf("%d", u.UpdateID),
		Platform:   core.PlatformTelegram,
		ReceivedAt: time.Now(),
	}
	switch {
	case u.Message != nil:
		out.Type = core.UpdateTypeMessage
		out.Chat = u.Message.Chat.toCoreChat()
		if u.Message.From != nil {
			out.From = u.Message.From.toCoreAccount()
		}
		out.Message = u.Message.toIncoming()
	case u.EditedMessage != nil:
		out.Type = core.UpdateTypeEditedMessage
		out.Chat = u.EditedMessage.Chat.toCoreChat()
		if u.EditedMessage.From != nil {
			out.From = u.EditedMessage.From.toCoreAccount()
		}
		out.Message = u.EditedMessage.toIncoming()
	case u.CallbackQuery != nil:
		out.Type = core.UpdateTypeCallback
		out.From = u.CallbackQuery.From.toCoreAccount()
		if u.CallbackQuery.Message != nil {
			out.Chat = u.CallbackQuery.Message.Chat.toCoreChat()
		}
		out.Callback = &core.CallbackQuery{
			ID:        u.CallbackQuery.ID,
			Data:      u.CallbackQuery.Data,
			MessageID: messageIDOf(u.CallbackQuery.Message),
		}
	case u.MessageReaction != nil:
		out.Type = core.UpdateTypeReaction
		out.Chat = u.MessageReaction.Chat.toCoreChat()
		out.From = u.MessageReaction.User.toCoreAccount()
		emoji := ""
		added := len(u.MessageReaction.NewReaction) > 0
		if added {
			emoji = u.MessageReaction.NewReaction[0].Emoji
		} else if len(u.MessageReaction.OldReaction) > 0 {
			emoji = u.MessageReaction.OldReaction[0].Emoji
		}
		out.Reaction = &core.Reaction{
			MessageID: fmt.Sprintf("%d", u.MessageReaction.MessageID),
			Emoji:     emoji,
			Added:     added,
		}
	case u.ChatMember != nil:
		out.Type = core.UpdateTypeMembership
		out.Chat = u.ChatMember.Chat.toCoreChat()
		out.From = u.ChatMember.From.toCoreAccount()
		out.Membership = &core.MembershipChange{
			ChatID:    fmt.Sprintf("%d", u.ChatMember.Chat.ID),
			MemberID:  fmt.Sprintf("%d", u.ChatMember.NewChatMember.User.ID),
			OldStatus: u.ChatMember.OldChatMember.Status,
			NewStatus: u.ChatMember.NewChatMember.Status,
		}
	default:
		out.Type = core.UpdateTypeUnknown
	}
	return out
}

func messageIDOf(m *telegramMessage) string {
	if m == nil {
		return ""
	}
	return fmt.Sprintf("%d", m.MessageID)
}

func (c telegramChat) toCoreChat() core.Chat {
	chatType := core.ChatTypePrivate
	switch c.Type {
	case "private":
		chatType = core.ChatTypePrivate
	case "group":
		chatType = core.ChatTypeGroup
	case "supergroup":
		chatType = core.ChatTypeSupergroup
	case "channel":
		chatType = core.ChatTypeChannel
	}
	return core.Chat{
		ID:       fmt.Sprintf("%d", c.ID),
		Platform: core.PlatformTelegram,
		Type:     chatType,
		Title:    c.Title,
		Username: c.Username,
	}
}

func (u telegramUser) toCoreAccount() core.Account {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	return core.Account{
		ID:          fmt.Sprintf("%d", u.ID),
		Platform:    core.PlatformTelegram,
		AccountType: core.AccountTypeBot,
		Username:    u.Username,
		DisplayName: name,
		IsBot:       u.IsBot,
	}
}

func (m *telegramMessage) toIncoming() *core.IncomingMessage {
	if m == nil {
		return nil
	}
	out := &core.IncomingMessage{
		MessageID: fmt.Sprintf("%d", m.MessageID),
		Text:      firstNonEmpty(m.Text, m.Caption),
		SentAt:    time.Unix(m.Date, 0),
	}
	if m.MessageThreadID != nil {
		out.ThreadID = fmt.Sprintf("%d", *m.MessageThreadID)
	}
	if m.ReplyToMessage != nil {
		out.ReplyTo = fmt.Sprintf("%d", m.ReplyToMessage.MessageID)
	}
	// Attachments — take the best single representative per kind.
	if len(m.Photo) > 0 {
		biggest := m.Photo[len(m.Photo)-1]
		out.Attachments = append(out.Attachments, core.Attachment{
			Type: core.AttachmentImage,
			FileRef: &core.FileRef{
				Platform: core.PlatformTelegram,
				ID:       biggest.FileID,
				Width:    biggest.Width,
				Height:   biggest.Height,
				Size:     biggest.FileSize,
			},
			Caption: m.Caption,
		})
	}
	if m.Document != nil {
		out.Attachments = append(out.Attachments, core.Attachment{
			Type:     core.AttachmentDocument,
			MimeType: m.Document.MimeType,
			FileName: m.Document.FileName,
			FileRef:  &core.FileRef{Platform: core.PlatformTelegram, ID: m.Document.FileID, Size: m.Document.FileSize, MimeType: m.Document.MimeType},
		})
	}
	if m.Voice != nil {
		out.Attachments = append(out.Attachments, core.Attachment{
			Type:     core.AttachmentVoice,
			MimeType: m.Voice.MimeType,
			FileRef: &core.FileRef{
				Platform: core.PlatformTelegram,
				ID:       m.Voice.FileID,
				Size:     m.Voice.FileSize,
				Duration: time.Duration(m.Voice.Duration) * time.Second,
				MimeType: m.Voice.MimeType,
			},
		})
	}
	if m.Audio != nil {
		out.Attachments = append(out.Attachments, core.Attachment{
			Type:     core.AttachmentAudio,
			MimeType: m.Audio.MimeType,
			FileName: m.Audio.FileName,
			FileRef: &core.FileRef{
				Platform: core.PlatformTelegram,
				ID:       m.Audio.FileID,
				Size:     m.Audio.FileSize,
				Duration: time.Duration(m.Audio.Duration) * time.Second,
				MimeType: m.Audio.MimeType,
			},
		})
	}
	if m.Video != nil {
		out.Attachments = append(out.Attachments, core.Attachment{
			Type:     core.AttachmentVideo,
			MimeType: m.Video.MimeType,
			FileRef: &core.FileRef{
				Platform: core.PlatformTelegram,
				ID:       m.Video.FileID,
				Width:    m.Video.Width,
				Height:   m.Video.Height,
				Size:     m.Video.FileSize,
				Duration: time.Duration(m.Video.Duration) * time.Second,
				MimeType: m.Video.MimeType,
			},
		})
	}
	if m.Location != nil {
		out.Attachments = append(out.Attachments, core.Attachment{
			Type:     core.AttachmentLocation,
			Location: &core.Location{Latitude: m.Location.Latitude, Longitude: m.Location.Longitude},
		})
	}
	if m.Contact != nil {
		out.Attachments = append(out.Attachments, core.Attachment{
			Type: core.AttachmentContact,
			Contact: &core.Contact{
				Name:        strings.TrimSpace(m.Contact.FirstName + " " + m.Contact.LastName),
				PhoneNumber: m.Contact.PhoneNumber,
				UserID:      fmt.Sprintf("%d", m.Contact.UserID),
			},
		})
	}
	return out
}
