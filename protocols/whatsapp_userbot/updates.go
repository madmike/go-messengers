package whatsapp_userbot

import (
	"context"
	"fmt"
	"time"

	"github.com/madmike/go-infra/telemetry"
	"github.com/madmike/go-messengers/core"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// handleEvent translates WhatsApp events to core.Update and dispatches them.
func (p *Protocol) handleEvent(evt interface{}) {
	switch e := evt.(type) {
	case *events.Message:
		// Suppress echoes of our own sent messages.
		if e.Info.IsFromMe {
			return
		}
		msgID := string(e.Info.ID)
		p.mu.Lock()
		if p.sentMsgIDs == nil {
			p.sentMsgIDs = make(map[string]struct{})
		}
		if _, isSentByUs := p.sentMsgIDs[msgID]; isSentByUs {
			p.mu.Unlock()
			return
		}
		// Deduplicate: WhatsApp multi-device can deliver the same message to multiple
		// device slots — only process each incoming message ID once.
		if p.seenMsgIDs == nil {
			p.seenMsgIDs = make(map[string]struct{})
		}
		if _, alreadySeen := p.seenMsgIDs[msgID]; alreadySeen {
			p.mu.Unlock()
			return
		}
		p.seenMsgIDs[msgID] = struct{}{}
		// Trim seenMsgIDs to avoid unbounded growth (keep newest 500).
		if len(p.seenMsgIDs) > 500 {
			p.seenMsgIDs = make(map[string]struct{})
		}
		p.mu.Unlock()

		p.handleNewMessage(context.Background(), e)

	case *events.LoggedOut:
		p.logger.Error("whatsapp_userbot: logged out (device removed)",
			telemetry.String("reason", e.Reason.String()),
			telemetry.String("phone", p.phone))
		if err := p.persistAuthFailure(context.Background(), fmt.Sprintf("logged out: %s", e.Reason.String())); err != nil {
			p.logger.Warn("persist auth failure", telemetry.Err(err))
		}

	case *events.ConnectFailure:
		switch e.Reason {
		case events.ConnectFailureLoggedOut,
			events.ConnectFailureMainDeviceGone,
			events.ConnectFailureUnknownLogout,
			events.ConnectFailureTempBanned:
			p.logger.Error("whatsapp_userbot: permanent connect failure",
				telemetry.String("reason", fmt.Sprintf("%d: %s", int(e.Reason), e.Message)),
				telemetry.String("phone", p.phone))
			if err := p.persistAuthFailure(context.Background(), fmt.Sprintf("connect failure %d: %s", int(e.Reason), e.Message)); err != nil {
				p.logger.Warn("persist auth failure", telemetry.Err(err))
			}
		default:
			p.logger.Warn("whatsapp_userbot: transient connect failure",
				telemetry.String("reason", fmt.Sprintf("%d: %s", int(e.Reason), e.Message)))
		}
	}
}

// handleNewMessage processes an incoming message: sends read receipt, shows typing
// indicator, then dispatches the update to the handler.
func (p *Protocol) handleNewMessage(ctx context.Context, evt *events.Message) {
	if evt == nil {
		return
	}

	chatJID := evt.Info.Chat
	senderJID := evt.Info.Sender

	// Mark message as read immediately.
	p.sendReadReceipt(ctx, chatJID, senderJID, string(evt.Info.ID), evt.Info.Timestamp)

	// Show typing indicator while the agent processes the message.
	p.sendTyping(ctx, chatJID)

	messageText := ""
	if evt.Message != nil {
		if conv := evt.Message.GetConversation(); conv != "" {
			messageText = conv
		} else if ext := evt.Message.GetExtendedTextMessage(); ext != nil {
			messageText = ext.GetText()
		}
	}

	chatType := core.ChatTypePrivate
	if chatJID.Server == types.GroupServer {
		chatType = core.ChatTypeGroup
	}

	update := core.Update{
		UpdateID:   fmt.Sprintf("%s_%s", chatJID.String(), string(evt.Info.ID)),
		Platform:   core.PlatformWhatsApp,
		ReceivedAt: time.Now(),
		Type:       core.UpdateTypeMessage,
		Chat: core.Chat{
			ID:       chatJID.String(),
			Platform: core.PlatformWhatsApp,
			Type:     chatType,
		},
		From: core.Account{
			ID:       senderJID.String(),
			Platform: core.PlatformWhatsApp,
		},
		Message: &core.IncomingMessage{
			MessageID: string(evt.Info.ID),
			Text:      messageText,
			SentAt:    evt.Info.Timestamp,
		},
	}

	p.mu.RLock()
	handler := p.handler
	p.mu.RUnlock()

	if handler != nil {
		handler(ctx, update)
	}
}
