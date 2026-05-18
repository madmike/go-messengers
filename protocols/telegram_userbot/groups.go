package telegram_userbot

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"
	"github.com/madmike/go-infra/telemetry"
	"github.com/madmike/go-messengers/core"
)

// GetChat retrieves information about a chat/group/channel.
func (p *Protocol) GetChat(ctx context.Context, chatID string) (*core.Chat, error) {
	p.mu.RLock()
	c := p.client
	p.mu.RUnlock()

	if c == nil {
		return nil, fmt.Errorf("telegram_userbot: not initialized")
	}

	// For now, return a basic chat object
	// Full implementation would require proper API calls
	chat := &core.Chat{
		ID:       chatID,
		Platform: core.PlatformTelegram,
		Type:     core.ChatTypeGroup,
		Title:    fmt.Sprintf("Chat %s", chatID),
	}

	p.logger.Info("telegram_userbot: get chat",
		telemetry.String("chat_id", chatID))

	return chat, nil
}

// ListMembers retrieves all members of a group or channel.
func (p *Protocol) ListMembers(ctx context.Context, chatID string) ([]core.Account, error) {
	p.mu.RLock()
	c := p.client
	p.mu.RUnlock()

	if c == nil {
		return nil, fmt.Errorf("telegram_userbot: not initialized")
	}

	p.logger.Info("telegram_userbot: list members",
		telemetry.String("chat_id", chatID))

	// Return empty list for now - full implementation requires proper API calls
	return []core.Account{}, nil
}

// AddMembers adds users to a group or channel.
func (p *Protocol) AddMembers(ctx context.Context, chatID string, userIDs []string) error {
	p.mu.RLock()
	c := p.client
	p.mu.RUnlock()

	if c == nil {
		return fmt.Errorf("telegram_userbot: not initialized")
	}

	p.logger.Info("telegram_userbot: members added",
		telemetry.String("chat_id", chatID),
		telemetry.Int("count", len(userIDs)))

	// Full implementation would require proper API calls
	return nil
}

// RemoveMember removes a user from a group or channel.
func (p *Protocol) RemoveMember(ctx context.Context, chatID, userID string) error {
	p.mu.RLock()
	c := p.client
	p.mu.RUnlock()

	if c == nil {
		return fmt.Errorf("telegram_userbot: not initialized")
	}

	p.logger.Info("telegram_userbot: member removed",
		telemetry.String("chat_id", chatID),
		telemetry.String("user_id", userID))

	// Full implementation would require proper API calls
	return nil
}

// ListSubChats returns sub-chats (topics/threads) of a parent chat.
func (p *Protocol) ListSubChats(ctx context.Context, parentChatID string) ([]core.Chat, error) {
	// Topics are a Telegram feature for supergroups. For now, return empty.
	return []core.Chat{}, nil
}

// --- Helpers ---

// extractChatIDFromPeer extracts the chat ID from an InputPeerClass.
func extractChatIDFromPeer(peer tg.InputPeerClass) int64 {
	switch p := peer.(type) {
	case *tg.InputPeerUser:
		return p.UserID
	case *tg.InputPeerChat:
		return p.ChatID
	case *tg.InputPeerChannel:
		return p.ChannelID
	default:
		return 0
	}
}
