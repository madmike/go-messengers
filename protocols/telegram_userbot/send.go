package telegram_userbot

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/madmike/go-infra/telemetry"
	"github.com/madmike/go-messengers/core"
)

// Send transmits a message to a Telegram chat via MTProto.
func (p *Protocol) Send(ctx context.Context, msg core.OutgoingMessage) (*core.SentMessage, error) {
	p.mu.RLock()
	c := p.client
	p.mu.RUnlock()

	if c == nil {
		return nil, fmt.Errorf("telegram_userbot: not initialized")
	}

	// Action-only message (no text, no buttons) — dispatch to the typing API.
	if msg.Action != "" && strings.TrimSpace(msg.Text) == "" && len(msg.Buttons) == 0 {
		if msg.Action == "typing" {
			peer, pErr := parseInputPeer(msg.ChatID)
			if pErr == nil {
				_, _ = c.API().MessagesSetTyping(ctx, &tg.MessagesSetTypingRequest{
					Peer:   peer,
					Action: &tg.SendMessageTypingAction{},
				})
			}
		}
		return &core.SentMessage{ChatID: msg.ChatID}, nil
	}

	// Validate message text is not empty (Telegram API rejects MESSAGE_EMPTY)
	if strings.TrimSpace(msg.Text) == "" {
		p.logger.Warn("telegram_userbot: skipping send with empty text",
			telemetry.String("chat_id", msg.ChatID))
		return nil, fmt.Errorf("message text is empty (AI generated no response)")
	}

	// Parse the chat ID to get the peer
	peer, err := parseInputPeer(msg.ChatID)
	if err != nil {
		return nil, fmt.Errorf("parse chat ID: %w", err)
	}

	// Generate a random ID for the message (required by Telegram)
	randID := make([]byte, 8)
	if _, err := rand.Read(randID); err != nil {
		return nil, fmt.Errorf("generate random ID: %w", err)
	}
	randomID := int64(binary.BigEndian.Uint64(randID))

	// Build the message request using the correct tg.MessagesSendMessageRequest
	req := &tg.MessagesSendMessageRequest{
		Peer:      peer,
		Message:   msg.Text,
		NoWebpage: msg.DisablePreview,
		Silent:    msg.Silent,
		RandomID:  randomID,
	}

	// If replying to a message, set the reply ID
	if msg.ReplyTo != "" {
		replyToID, err := parseMessageID(msg.ReplyTo)
		if err == nil {
			req.ReplyTo = &tg.InputReplyToMessage{
				ReplyToMsgID: replyToID,
			}
		}
	}

	// Send the message
	result, err := c.API().MessagesSendMessage(ctx, req)
	if err != nil {
		p.logger.Error("telegram_userbot: send failed",
			telemetry.String("chat_id", msg.ChatID),
			telemetry.Err(err))

		// If this is an auth error, mark account as failed
		if isAuthError(err) {
			_ = p.persistAuthFailure(context.Background(), fmt.Sprintf("send failed: %v", err))
		}
		return nil, fmt.Errorf("send message: %w", err)
	}

	// Extract message ID from result (handles both UpdateShortSentMessage and Updates)
	messageID := extractMessageID(result)
	if messageID == "" {
		return nil, fmt.Errorf("could not extract message ID from response")
	}

	p.logger.Info("telegram_userbot: message sent",
		telemetry.String("chat_id", msg.ChatID),
		telemetry.String("message_id", messageID))

	return &core.SentMessage{
		MessageID: messageID,
		ChatID:    msg.ChatID,
		SentAt:    time.Now(),
	}, nil
}

// Edit updates the text of an existing message.
func (p *Protocol) Edit(ctx context.Context, chatID, messageID, newText string) (*core.SentMessage, error) {
	p.mu.RLock()
	c := p.client
	p.mu.RUnlock()

	if c == nil {
		return nil, fmt.Errorf("telegram_userbot: not initialized")
	}

	peer, err := parseInputPeer(chatID)
	if err != nil {
		return nil, fmt.Errorf("parse chat ID: %w", err)
	}

	msgID, err := parseMessageID(messageID)
	if err != nil {
		return nil, fmt.Errorf("parse message ID: %w", err)
	}

	req := &tg.MessagesEditMessageRequest{
		Peer:    peer,
		ID:      msgID,
		Message: newText,
	}

	_, err = c.API().MessagesEditMessage(ctx, req)
	if err != nil {
		p.logger.Error("telegram_userbot: edit failed",
			telemetry.String("chat_id", chatID),
			telemetry.String("message_id", messageID),
			telemetry.Err(err))
		return nil, fmt.Errorf("edit message: %w", err)
	}

	p.logger.Info("telegram_userbot: message edited",
		telemetry.String("chat_id", chatID),
		telemetry.String("message_id", messageID))

	return &core.SentMessage{
		MessageID: messageID,
		ChatID:    chatID,
		SentAt:    time.Now(),
	}, nil
}

// Delete removes a message from a chat.
func (p *Protocol) Delete(ctx context.Context, chatID, messageID string) error {
	p.mu.RLock()
	c := p.client
	p.mu.RUnlock()

	if c == nil {
		return fmt.Errorf("telegram_userbot: not initialized")
	}

	_, err := parseInputPeer(chatID)
	if err != nil {
		return fmt.Errorf("parse chat ID: %w", err)
	}

	msgID, err := parseMessageID(messageID)
	if err != nil {
		return fmt.Errorf("parse message ID: %w", err)
	}

	req := &tg.MessagesDeleteMessagesRequest{
		ID: []int{msgID},
	}

	_, err = c.API().MessagesDeleteMessages(ctx, req)
	if err != nil {
		p.logger.Error("telegram_userbot: delete failed",
			telemetry.String("chat_id", chatID),
			telemetry.String("message_id", messageID),
			telemetry.Err(err))
		return fmt.Errorf("delete message: %w", err)
	}

	p.logger.Info("telegram_userbot: message deleted",
		telemetry.String("chat_id", chatID),
		telemetry.String("message_id", messageID))

	return nil
}

// --- Helpers ---

// parseInputPeer converts a chat ID string to an InputPeerClass.
// For now, supports user/chat IDs in format: "123" or "user:123" or "chat:123" or "channel:123"
func parseInputPeer(chatID string) (tg.InputPeerClass, error) {
	// For simplicity, assume it's a user ID by default
	var id int64
	_, err := fmt.Sscanf(chatID, "%d", &id)
	if err != nil {
		// Try to parse with prefix
		var prefix string
		_, err := fmt.Sscanf(chatID, "%s:%d", &prefix, &id)
		if err != nil {
			return nil, fmt.Errorf("invalid chat ID format: %s", chatID)
		}

		switch prefix {
		case "user":
			return &tg.InputPeerUser{UserID: id, AccessHash: 0}, nil
		case "chat":
			return &tg.InputPeerChat{ChatID: id}, nil
		case "channel":
			return &tg.InputPeerChannel{ChannelID: id, AccessHash: 0}, nil
		default:
			return nil, fmt.Errorf("unknown peer type: %s", prefix)
		}
	}

	// Default to user ID
	return &tg.InputPeerUser{UserID: id, AccessHash: 0}, nil
}

// parseMessageID converts a message ID string to an int.
func parseMessageID(msgID string) (int, error) {
	var id int
	_, err := fmt.Sscanf(msgID, "%d", &id)
	return id, err
}

// extractMessageID extracts the message ID from a Telegram API response.
func extractMessageID(result tg.UpdatesClass) string {
	switch u := result.(type) {
	case *tg.UpdateShortSentMessage:
		return fmt.Sprintf("%d", u.ID)
	case *tg.Updates:
		// Look for UpdateNewMessage or similar
		for _, update := range u.Updates {
			switch upd := update.(type) {
			case *tg.UpdateNewMessage:
				if msg, ok := upd.Message.(*tg.Message); ok {
					return fmt.Sprintf("%d", msg.ID)
				}
			}
		}
	case *tg.UpdatesCombined:
		for _, update := range u.Updates {
			switch upd := update.(type) {
			case *tg.UpdateNewMessage:
				if msg, ok := upd.Message.(*tg.Message); ok {
					return fmt.Sprintf("%d", msg.ID)
				}
			}
		}
	}
	return ""
}
