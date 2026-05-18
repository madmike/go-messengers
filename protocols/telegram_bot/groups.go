package telegram_bot

import (
	"context"
	"fmt"

	"github.com/madmike/go-messengers/core"
)

// GetChat implements core.GroupManager.
func (p *Protocol) GetChat(ctx context.Context, chatID string) (*core.Chat, error) {
	body := map[string]any{"chat_id": chatID}
	var resp struct {
		ID              int64    `json:"id"`
		Type            string   `json:"type"`
		Title           string   `json:"title"`
		Username        string   `json:"username"`
		IsForum         bool     `json:"is_forum"`
		ActiveUsernames []string `json:"active_usernames"`
	}
	if err := p.apiCall(ctx, "getChat", body, &resp); err != nil {
		return nil, err
	}
	c := telegramChat{
		ID:       resp.ID,
		Type:     resp.Type,
		Title:    resp.Title,
		Username: resp.Username,
		IsForum:  resp.IsForum,
	}.toCoreChat()
	return &c, nil
}

// ListMembers is not exposed by the Bot API for privacy; only administrators
// can be enumerated. We return them via getChatAdministrators for parity.
func (p *Protocol) ListMembers(ctx context.Context, chatID string) ([]core.Account, error) {
	body := map[string]any{"chat_id": chatID}
	var resp []struct {
		User telegramUser `json:"user"`
	}
	if err := p.apiCall(ctx, "getChatAdministrators", body, &resp); err != nil {
		return nil, err
	}
	out := make([]core.Account, 0, len(resp))
	for _, row := range resp {
		out = append(out, row.User.toCoreAccount())
	}
	return out, nil
}

// AddMembers is not supported by the Bot API (bots can't invite users).
func (p *Protocol) AddMembers(ctx context.Context, chatID string, userIDs []string) error {
	return fmt.Errorf("telegram_bot: AddMembers is not available for bot accounts; use a userbot")
}

// RemoveMember kicks a user via banChatMember. Admin privilege required.
func (p *Protocol) RemoveMember(ctx context.Context, chatID, userID string) error {
	body := map[string]any{
		"chat_id": chatID,
		"user_id": userID,
	}
	return p.apiCall(ctx, "banChatMember", body, nil)
}

// ListSubChats enumerates forum topics for a supergroup. The Bot API does not
// expose a list-all-topics method, so this returns an empty slice unless the
// caller has cached topic IDs (via UpdateTypeMessage.ThreadID events).
//
// Kept in the interface so future integrations (userbots, MTProto) can
// implement it fully without churning callers.
func (p *Protocol) ListSubChats(ctx context.Context, parentChatID string) ([]core.Chat, error) {
	return nil, nil
}
