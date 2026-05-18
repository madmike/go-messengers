package telegram_userbot

import (
	"context"
	"fmt"
	"time"

	"github.com/gotd/td/tg"
	"github.com/madmike/go-infra/telemetry"
	"github.com/madmike/go-messengers/core"
)

// UpdateDispatcher implements gotd/td's update handling interface.
type UpdateDispatcher struct {
	protocol *Protocol
	handler  core.UpdateHandler
	logger   telemetry.Logger
}

// Handle processes a single MTProto update and dispatches it to the handler.
func (d *UpdateDispatcher) Handle(ctx context.Context, u tg.UpdateClass) error {
	if u == nil {
		return nil
	}

	switch upd := u.(type) {
	case *tg.UpdateNewMessage:
		d.handleNewMessage(ctx, upd.Message)
	case *tg.UpdateNewChannelMessage:
		d.handleNewChannelMessage(ctx, upd.Message)
	case *tg.UpdateEditChannelMessage:
		d.handleEditedMessage(ctx, upd.Message)
	case *tg.UpdateMessageReactions:
		d.handleReaction(ctx, upd)
	case *tg.UpdateChatParticipant:
		d.handleMembershipChange(ctx, upd)
	}
	return nil
}


// handleNewMessage processes UpdateNewMessage.
func (d *UpdateDispatcher) handleNewMessage(ctx context.Context, msgRaw tg.MessageClass) {
	msg, ok := msgRaw.(*tg.Message)
	if !ok || msg == nil {
		return // Skip non-message types (service messages, etc.)
	}

	// Skip messages sent by the authenticated user (Out=true). When the userbot
	// sends or the user sends from another device, Telegram echoes the update
	// back with Out=true. Processing these as inbound would cause the AI to reply
	// to its own messages and the user's outgoing messages — creating a loop.
	if msg.Out {
		return
	}

	// Only process direct messages (PeerUser)
	if _, isDirect := msg.PeerID.(*tg.PeerUser); !isDirect {
		return // Skip group and channel messages
	}

	// Extract chat ID from peer
	chatID := extractPeerID(msg.PeerID)
	fromID := extractPeerID(msg.FromID)

	update := core.Update{
		UpdateID:   fmt.Sprintf("%d", msg.ID),
		Platform:   core.PlatformTelegram,
		ReceivedAt: time.Unix(int64(msg.Date), 0),
		Type:       core.UpdateTypeMessage,
		Chat: core.Chat{
			ID:       chatID,
			Platform: core.PlatformTelegram,
			Type:     chatTypeForPeer(msg.PeerID),
		},
		From: core.Account{
			ID:       fromID,
			Platform: core.PlatformTelegram,
		},
		Message: &core.IncomingMessage{
			MessageID: fmt.Sprintf("%d", msg.ID),
			Text:      msg.Message,
			SentAt:    time.Unix(int64(msg.Date), 0),
		},
	}

	// Extract attachments if present
	if msg.Media != nil {
		atts, err := extractAttachments(msg.Media)
		if err == nil {
			update.Message.Attachments = atts
		}
	}

	// Populate reply-to if this is a reply
	if msg.ReplyTo != nil {
		if r, ok := msg.ReplyTo.(*tg.MessageReplyHeader); ok && r != nil {
			update.Message.ReplyTo = fmt.Sprintf("%d", r.ReplyToMsgID)
		}
	}

	// Dispatch the normalized update
	d.handler(ctx, update)
}

// handleNewChannelMessage processes UpdateNewChannelMessage.
func (d *UpdateDispatcher) handleNewChannelMessage(ctx context.Context, msgRaw tg.MessageClass) {
	msg, ok := msgRaw.(*tg.Message)
	if !ok || msg == nil {
		return
	}

	// For channel messages, treat them as regular messages for now.
	// In the future, we could set ChatType to ChatTypeChannel.
	d.handleNewMessage(ctx, msg)
}

// handleEditedMessage processes UpdateEditChannelMessage.
func (d *UpdateDispatcher) handleEditedMessage(ctx context.Context, msgRaw tg.MessageClass) {
	msg, ok := msgRaw.(*tg.Message)
	if !ok || msg == nil {
		return
	}

	// Skip edits to outgoing messages (sent by the authenticated user).
	if msg.Out {
		return
	}

	chatID := extractPeerID(msg.PeerID)
	fromID := extractPeerID(msg.FromID)

	update := core.Update{
		UpdateID:   fmt.Sprintf("%d_edited", msg.ID),
		Platform:   core.PlatformTelegram,
		ReceivedAt: time.Now(),
		Type:       core.UpdateTypeEditedMessage,
		Chat: core.Chat{
			ID:       chatID,
			Platform: core.PlatformTelegram,
			Type:     chatTypeForPeer(msg.PeerID),
		},
		From: core.Account{
			ID:       fromID,
			Platform: core.PlatformTelegram,
		},
		Message: &core.IncomingMessage{
			MessageID: fmt.Sprintf("%d", msg.ID),
			Text:      msg.Message,
			EditedAt:  time.Now(),
		},
	}

	d.handler(ctx, update)
}

// handleReaction processes UpdateMessageReactions.
func (d *UpdateDispatcher) handleReaction(ctx context.Context, u *tg.UpdateMessageReactions) {
	if u == nil {
		return
	}

	chatID := extractPeerID(u.Peer)
	msgID := fmt.Sprintf("%d", u.MsgID)

	// For now, dispatch the first reaction as a sample.
	// A full implementation might batch or aggregate reactions.
	if len(u.Reactions.Results) > 0 {
		result := u.Reactions.Results[0]
		emoji := extractReactionEmoji(result.Reaction)
		if emoji == "" {
			return // Skip if we can't extract the emoji
		}

		update := core.Update{
			UpdateID:   fmt.Sprintf("%s_reaction_%s", msgID, emoji),
			Platform:   core.PlatformTelegram,
			ReceivedAt: time.Now(),
			Type:       core.UpdateTypeReaction,
			Chat: core.Chat{
				ID:       chatID,
				Platform: core.PlatformTelegram,
				Type:     chatTypeForPeer(u.Peer),
			},
			Reaction: &core.Reaction{
				MessageID: msgID,
				Emoji:     emoji,
				Added:     true,
			},
		}

		d.handler(ctx, update)
	}
}

// handleMembershipChange processes UpdateChatParticipant.
func (d *UpdateDispatcher) handleMembershipChange(ctx context.Context, u *tg.UpdateChatParticipant) {
	if u == nil {
		return
	}

	chatID := fmt.Sprintf("%d", u.ChatID)
	userID := fmt.Sprintf("%d", u.UserID)

	// Determine membership status change
	oldStatus, newStatus := statusStrings(u.PrevParticipant, u.NewParticipant)

	update := core.Update{
		UpdateID:   fmt.Sprintf("%s_%s_membership_%s", chatID, userID, newStatus),
		Platform:   core.PlatformTelegram,
		ReceivedAt: time.Now(),
		Type:       core.UpdateTypeMembership,
		Chat: core.Chat{
			ID:       chatID,
			Platform: core.PlatformTelegram,
			Type:     core.ChatTypeGroup,
		},
		Membership: &core.MembershipChange{
			ChatID:    chatID,
			MemberID:  userID,
			OldStatus: oldStatus,
			NewStatus: newStatus,
		},
	}

	d.handler(ctx, update)
}

// --- Helpers ---

// extractPeerID extracts the numeric ID from a Telegram PeerClass.
func extractPeerID(peer tg.PeerClass) string {
	if peer == nil {
		return "0"
	}
	switch p := peer.(type) {
	case *tg.PeerUser:
		return fmt.Sprintf("%d", p.UserID)
	case *tg.PeerChat:
		return fmt.Sprintf("%d", p.ChatID)
	case *tg.PeerChannel:
		return fmt.Sprintf("%d", p.ChannelID)
	default:
		return "0"
	}
}

// extractReactionEmoji extracts the emoji string from a ReactionClass.
func extractReactionEmoji(reaction tg.ReactionClass) string {
	if reaction == nil {
		return ""
	}
	switch r := reaction.(type) {
	case *tg.ReactionEmoji:
		return r.Emoticon
	case *tg.ReactionCustomEmoji:
		// Custom emoji - for now just use a placeholder
		return "👍" // Fallback emoji
	default:
		return ""
	}
}

// chatTypeForPeer determines the core.ChatType from a Telegram PeerID.
func chatTypeForPeer(peer tg.PeerClass) core.ChatType {
	switch peer.(type) {
	case *tg.PeerUser:
		return core.ChatTypePrivate
	case *tg.PeerChat:
		return core.ChatTypeGroup
	case *tg.PeerChannel:
		return core.ChatTypeSupergroup // Telegram channels are supergroups for our purposes
	default:
		return core.ChatTypePrivate
	}
}

// extractAttachments extracts core.Attachment from Telegram media.
func extractAttachments(mediaRaw tg.MessageMediaClass) ([]core.Attachment, error) {
	var attachments []core.Attachment

	switch media := mediaRaw.(type) {
	case *tg.MessageMediaPhoto:
		if media.Photo != nil {
			att := core.Attachment{
				Type: core.AttachmentImage,
			}
			attachments = append(attachments, att)
		}
	case *tg.MessageMediaDocument:
		if doc, ok := media.Document.(*tg.Document); ok && doc != nil {
			mimeType := doc.MimeType
			attType := core.AttachmentDocument
			if mimeType == "audio/ogg" {
				attType = core.AttachmentVoice
			} else if mimeType == "video/mp4" {
				attType = core.AttachmentVideo
			}

			att := core.Attachment{
				Type:     attType,
				MimeType: mimeType,
			}
			attachments = append(attachments, att)
		}
	}

	return attachments, nil
}

// statusStrings converts Telegram participant classes to status strings.
func statusStrings(oldParticipant, newParticipant tg.ChatParticipantClass) (string, string) {
	oldStatus := participantStatusString(oldParticipant)
	newStatus := participantStatusString(newParticipant)
	return oldStatus, newStatus
}

// participantStatusString returns a string representation of a participant status.
func participantStatusString(p tg.ChatParticipantClass) string {
	if p == nil {
		return "left"
	}

	switch p.(type) {
	case *tg.ChatParticipant:
		return "member"
	case *tg.ChatParticipantCreator:
		return "creator"
	case *tg.ChatParticipantAdmin:
		return "admin"
	default:
		return "unknown"
	}
}
