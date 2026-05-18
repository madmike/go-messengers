package whatsapp_userbot

import (
	"context"
	"fmt"

	"github.com/madmike/go-infra/telemetry"
	"github.com/madmike/go-messengers/core"
)

// GetChat retrieves information about a chat/group.
func (p *Protocol) GetChat(ctx context.Context, chatID string) (*core.Chat, error) {
	p.mu.RLock()
	c := p.client
	p.mu.RUnlock()

	if c == nil {
		return nil, fmt.Errorf("whatsapp_userbot: not initialized")
	}

	// Return basic chat info for now
	chat := &core.Chat{
		ID:       chatID,
		Platform: core.PlatformWhatsApp,
		Type:     core.ChatTypePrivate,
		Title:    chatID,
	}

	p.logger.Info("whatsapp_userbot: get chat",
		telemetry.String("chat_id", chatID))

	return chat, nil
}

// ListMembers retrieves all members of a group.
func (p *Protocol) ListMembers(ctx context.Context, chatID string) ([]core.Account, error) {
	p.mu.RLock()
	c := p.client
	p.mu.RUnlock()

	if c == nil {
		return nil, fmt.Errorf("whatsapp_userbot: not initialized")
	}

	p.logger.Info("whatsapp_userbot: list members",
		telemetry.String("chat_id", chatID))

	// Return empty list for now - full implementation requires proper API calls
	return []core.Account{}, nil
}

// AddMembers adds users to a group.
func (p *Protocol) AddMembers(ctx context.Context, chatID string, userIDs []string) error {
	p.mu.RLock()
	c := p.client
	p.mu.RUnlock()

	if c == nil {
		return fmt.Errorf("whatsapp_userbot: not initialized")
	}

	p.logger.Info("whatsapp_userbot: members added",
		telemetry.String("chat_id", chatID),
		telemetry.Int("count", len(userIDs)))

	// Full implementation would require proper API calls
	return nil
}

// RemoveMember removes a user from a group.
func (p *Protocol) RemoveMember(ctx context.Context, chatID, userID string) error {
	p.mu.RLock()
	c := p.client
	p.mu.RUnlock()

	if c == nil {
		return fmt.Errorf("whatsapp_userbot: not initialized")
	}

	p.logger.Info("whatsapp_userbot: member removed",
		telemetry.String("chat_id", chatID),
		telemetry.String("user_id", userID))

	// Full implementation would require proper API calls
	return nil
}

// ListSubChats returns sub-chats (threads) of a parent chat.
func (p *Protocol) ListSubChats(ctx context.Context, parentChatID string) ([]core.Chat, error) {
	// WhatsApp doesn't support threads like Telegram, return empty for now
	return []core.Chat{}, nil
}
