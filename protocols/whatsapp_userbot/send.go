package whatsapp_userbot

import (
	"context"
	"fmt"
	"time"

	"github.com/madmike/go-infra/telemetry"
	"github.com/madmike/go-messengers/core"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// Send transmits a message to a WhatsApp chat.
// If msg.Action is "typing" and no text is present, a composing presence
// indicator is sent instead of a message.
func (p *Protocol) Send(ctx context.Context, msg core.OutgoingMessage) (*core.SentMessage, error) {
	p.mu.RLock()
	c := p.client
	p.mu.RUnlock()

	if c == nil {
		return nil, fmt.Errorf("whatsapp_userbot: not initialized")
	}
	if !c.IsLoggedIn() {
		return nil, fmt.Errorf("whatsapp_userbot: not logged in")
	}

	chatJID, err := types.ParseJID(msg.ChatID)
	if err != nil {
		return nil, fmt.Errorf("whatsapp_userbot: invalid chat JID %q: %w", msg.ChatID, err)
	}

	// Action-only message (no text, no buttons) — dispatch to the correct presence API.
	if msg.Action != "" && msg.Text == "" && len(msg.Buttons) == 0 {
		if msg.Action == "typing" {
			p.sendTyping(ctx, chatJID)
		}
		return &core.SentMessage{ChatID: msg.ChatID}, nil
	}

	waMsg := &waE2E.Message{
		Conversation: proto.String(msg.Text),
	}

	resp, err := c.SendMessage(ctx, chatJID, waMsg)
	if err != nil {
		return nil, fmt.Errorf("whatsapp_userbot: send: %w", err)
	}

	p.logger.Info("whatsapp_userbot: message sent",
		telemetry.String("chat_id", msg.ChatID),
		telemetry.String("msg_id", string(resp.ID)))

	// Track the sent message ID so echoes of our own messages are filtered in handleEvent.
	p.mu.Lock()
	if p.sentMsgIDs == nil {
		p.sentMsgIDs = make(map[string]struct{})
	}
	p.sentMsgIDs[string(resp.ID)] = struct{}{}
	p.mu.Unlock()

	return &core.SentMessage{
		MessageID: string(resp.ID),
		ChatID:    msg.ChatID,
		SentAt:    resp.Timestamp,
	}, nil
}

// Edit updates the text of an existing message.
func (p *Protocol) Edit(ctx context.Context, chatID, messageID, newText string) (*core.SentMessage, error) {
	p.mu.RLock()
	c := p.client
	p.mu.RUnlock()

	if c == nil {
		return nil, fmt.Errorf("whatsapp_userbot: not initialized")
	}
	if !c.IsLoggedIn() {
		return nil, fmt.Errorf("whatsapp_userbot: not logged in")
	}

	// WhatsApp edit uses ProtocolMessage with MESSAGE_EDIT type; fall back to
	// sending a new message with the updated text for now.
	chatJID, err := types.ParseJID(chatID)
	if err != nil {
		return nil, fmt.Errorf("whatsapp_userbot: invalid chat JID %q: %w", chatID, err)
	}

	waMsg := &waE2E.Message{
		Conversation: proto.String(newText),
	}
	resp, err := c.SendMessage(ctx, chatJID, waMsg)
	if err != nil {
		return nil, fmt.Errorf("whatsapp_userbot: edit (send replacement): %w", err)
	}

	p.logger.Info("whatsapp_userbot: message edited",
		telemetry.String("chat_id", chatID),
		telemetry.String("message_id", messageID))

	return &core.SentMessage{
		MessageID: string(resp.ID),
		ChatID:    chatID,
		SentAt:    resp.Timestamp,
	}, nil
}

// Delete revokes a sent message.
func (p *Protocol) Delete(ctx context.Context, chatID, messageID string) error {
	p.mu.RLock()
	c := p.client
	p.mu.RUnlock()

	if c == nil {
		return fmt.Errorf("whatsapp_userbot: not initialized")
	}
	if !c.IsLoggedIn() {
		return fmt.Errorf("whatsapp_userbot: not logged in")
	}

	chatJID, err := types.ParseJID(chatID)
	if err != nil {
		return fmt.Errorf("whatsapp_userbot: invalid chat JID %q: %w", chatID, err)
	}

	if _, err := c.RevokeMessage(ctx, chatJID, types.MessageID(messageID)); err != nil {
		return fmt.Errorf("whatsapp_userbot: revoke: %w", err)
	}

	p.logger.Info("whatsapp_userbot: message deleted",
		telemetry.String("chat_id", chatID),
		telemetry.String("message_id", messageID))

	return nil
}

// sendReadReceipt marks the given message as read.
func (p *Protocol) sendReadReceipt(ctx context.Context, chatJID, senderJID types.JID, msgID string, ts time.Time) {
	c := p.client
	if c == nil {
		return
	}
	if err := c.MarkRead(ctx, []types.MessageID{types.MessageID(msgID)}, ts, chatJID, senderJID); err != nil {
		p.logger.Warn("whatsapp_userbot: mark read failed", telemetry.Err(err))
	}
}

// sendTyping emits a composing presence indicator for the given chat.
func (p *Protocol) sendTyping(ctx context.Context, chatJID types.JID) {
	c := p.client
	if c == nil {
		return
	}
	if err := c.SendChatPresence(ctx, chatJID, types.ChatPresenceComposing, types.ChatPresenceMediaText); err != nil {
		p.logger.Warn("whatsapp_userbot: send typing failed", telemetry.Err(err))
	}
}
