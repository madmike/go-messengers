package core

import "time"

// ChatType classifies a conversation target.
type ChatType string

const (
	ChatTypePrivate    ChatType = "private"
	ChatTypeGroup      ChatType = "group"
	ChatTypeSupergroup ChatType = "supergroup" // Telegram supergroup, WA community
	ChatTypeChannel    ChatType = "channel"    // Telegram channel, WA broadcast
	ChatTypeTopic      ChatType = "topic"      // Telegram forum topic, sub-chat
	ChatTypeBusiness   ChatType = "business"   // WA Business conversation
)

// Chat represents a conversation target. Groups and sub-groups form a
// hierarchy via ParentID (e.g. a Telegram forum topic's parent is its
// supergroup; a WA group's parent is its community).
type Chat struct {
	ID          string
	Platform    Platform
	Type        ChatType
	Title       string
	Username    string // @handle or empty for private chats
	ParentID    string // non-empty for topics / sub-groups
	MemberCount int
	IsVerified  bool
	RawMetadata map[string]any
}

// Attachment types.
type AttachmentType string

const (
	AttachmentImage    AttachmentType = "image"
	AttachmentVideo    AttachmentType = "video"
	AttachmentAudio    AttachmentType = "audio"
	AttachmentVoice    AttachmentType = "voice"
	AttachmentDocument AttachmentType = "document"
	AttachmentSticker  AttachmentType = "sticker"
	AttachmentLocation AttachmentType = "location"
	AttachmentContact  AttachmentType = "contact"
)

// Attachment is a single piece of media or structured payload attached to a
// message. Exactly one of FileRef / InlineData / Location / Contact is set.
type Attachment struct {
	Type     AttachmentType
	MimeType string
	FileName string

	FileRef    *FileRef // Provider-side file id (already uploaded)
	InlineData []byte   // Raw bytes — provider will upload on Send()
	Location   *Location
	Contact    *Contact

	// Caption or transcript (for voice notes after STT, if populated)
	Caption string
}

// FileRef is a stable handle for a file hosted by the provider.
type FileRef struct {
	Platform Platform
	ID       string // Telegram file_id, WA media_id
	URL      string // Signed URL when available
	Size     int64
	Width    int // for images/video
	Height   int
	Duration time.Duration // for audio/video
	MimeType string
}

// FileUpload describes a file to upload to the provider's media service.
type FileUpload struct {
	Data     []byte
	MimeType string
	FileName string
}

// Location / Contact are two structured non-file attachment payloads.
type Location struct {
	Latitude  float64
	Longitude float64
	Accuracy  float64
	Title     string
	Address   string
}

type Contact struct {
	Name        string
	PhoneNumber string
	UserID      string // provider user ID if resolvable
}

// OutgoingMessage is what Messenger.Send accepts. Text + Attachments are
// independent; either or both may be set.
type OutgoingMessage struct {
	ChatID      string
	ThreadID    string // topic / reply thread id (Telegram forum topic, WA business reply)
	Text        string
	ParseMode   ParseMode // formatting hint
	ReplyTo     string    // message id being replied to
	Attachments []Attachment

	// Templating (WA templates, Viber rich media, …). Opaque JSON bag passed
	// straight to the provider.
	Template map[string]any

	// Inline keyboard / quick replies. Provider implementations translate
	// each Button into their native representation.
	Buttons [][]Button

	// Disable link previews, notifications, etc.
	DisablePreview      bool
	DisableNotification bool
	Silent              bool
	Action              string // typing | upload_photo | ...

	// Arbitrary extras (business_account_id, from_phone_id, custom_sender, …)
	Options map[string]any
}

// ParseMode hints at how Text is formatted.
type ParseMode string

const (
	ParseModePlain      ParseMode = ""
	ParseModeMarkdown   ParseMode = "markdown"
	ParseModeMarkdownV2 ParseMode = "markdown_v2"
	ParseModeHTML       ParseMode = "html"
)

// Button is a tap-able action attached to an outgoing message.
type Button struct {
	Text         string
	CallbackData string // inline data echoed back on tap
	URL          string // open external URL
	SwitchInline string // Telegram "switch inline query"
}

// SentMessage is the ack returned by Messenger.Send.
type SentMessage struct {
	MessageID   string
	ChatID      string
	SentAt      time.Time
	RawResponse map[string]any
}

// Update is the normalized inbound event every Receiver emits. Protocol-
// specific extras live in RawPayload.
type Update struct {
	UpdateID   string
	Platform   Platform
	ReceivedAt time.Time
	Type       UpdateType
	Chat       Chat
	From       Account
	Message    *IncomingMessage  // populated for UpdateTypeMessage / ::Edited
	Callback   *CallbackQuery    // populated for UpdateTypeCallback
	Reaction   *Reaction         // populated for UpdateTypeReaction
	Membership *MembershipChange // populated for UpdateTypeMembership
	RawPayload map[string]any
}

// UpdateType enumerates the canonical update classes. Unknown provider-
// specific events collapse to UpdateTypeUnknown with RawPayload populated.
type UpdateType string

const (
	UpdateTypeMessage       UpdateType = "message"
	UpdateTypeEditedMessage UpdateType = "edited_message"
	UpdateTypeCallback      UpdateType = "callback"
	UpdateTypeReaction      UpdateType = "reaction"
	UpdateTypeMembership    UpdateType = "membership"
	UpdateTypePresence      UpdateType = "presence"
	UpdateTypeUnknown       UpdateType = "unknown"
)

// IncomingMessage is the normalized shape of a received text/media message.
type IncomingMessage struct {
	MessageID     string
	ThreadID      string
	Text          string
	Attachments   []Attachment
	ReplyTo       string
	ForwardedFrom *Account
	SentAt        time.Time
	EditedAt      time.Time
}

// CallbackQuery is a tap on an inline button.
type CallbackQuery struct {
	ID          string
	MessageID   string
	Data        string
	InlineQuery string // Telegram-specific
}

// Reaction is a tap-to-react event.
type Reaction struct {
	MessageID string
	Emoji     string
	Added     bool // false = removed
}

// MembershipChange covers join/leave/promote/demote events.
type MembershipChange struct {
	ChatID    string
	MemberID  string
	OldStatus string
	NewStatus string
}
