// Package core defines the protocol-agnostic interfaces every messenger
// integration implements. Two axes of variation are captured:
//
//   - Account type: official bot/business API vs userbot (real user account
//     driven headlessly via MTProto / whatsmeow).
//   - Capability: Messenger (outbound), Receiver (inbound), GroupManager
//     (chat membership), FileManager (media upload/download).
//
// Callers type-assert against the capability interfaces they need, mirroring
// the go-ai-providers design.
package core

import (
	"context"
	"time"
)

// Platform identifies the messaging network.
type Platform string

const (
	PlatformTelegram          Platform = "telegram"
	PlatformWhatsApp          Platform = "whatsapp"
	PlatformViber             Platform = "viber"
	PlatformVK                Platform = "vk"
	PlatformFacebookMessenger Platform = "facebook_messenger"
	PlatformInstagram         Platform = "instagram"
)

// AccountType distinguishes authentication & privilege models. Bots / official
// business APIs are rate-limited and sanctioned; userbots impersonate a real
// user account and come with additional ToS constraints.
type AccountType string

const (
	AccountTypeBot         AccountType = "bot"          // Telegram Bot, WhatsApp Cloud API
	AccountTypeBusinessAPI AccountType = "business_api" // WA Business on-premise, Instagram Graph
	AccountTypeUserbot     AccountType = "userbot"      // MTProto user session, whatsmeow pairing
)

// Capability enumerates what a Provider can do. Used for registry routing.
type Capability string

const (
	CapabilitySend       Capability = "send"
	CapabilityReceive    Capability = "receive"
	CapabilityGroups     Capability = "groups"
	CapabilityFiles      Capability = "files"
	CapabilityReactions  Capability = "reactions"
	CapabilityPresence   Capability = "presence"
	CapabilityEditDelete Capability = "edit_delete"
)

// Provider is the base interface every messenger backend implements.
type Provider interface {
	Name() string
	Platform() Platform
	AccountType() AccountType

	Initialize(ctx context.Context, config ProviderConfig) error
	Close() error
	HealthCheck(ctx context.Context) error

	Capabilities() []Capability
	SupportsCapability(Capability) bool

	// Me returns the identity this provider authenticates as.
	Me(ctx context.Context) (*Account, error)
}

// ProviderConfig configures any messenger provider. Individual protocols
// read only the fields they care about.
type ProviderConfig struct {
	Name        string
	Platform    Platform
	AccountType AccountType

	// Bot / business API
	APIKey    string // Telegram bot token, WA permanent access token
	APISecret string // WA app secret for signed webhooks, FB app secret
	BaseURL   string

	// Userbot (MTProto / whatsmeow)
	AppID       int    // Telegram API ID
	AppHash     string // Telegram API hash
	Phone       string // Userbot phone (E.164)
	SessionPath string // Filesystem path / opaque id for the session store
	Password2FA string // Telegram 2FA cloud password (optional)


	// Account metadata (e.g. WA phone_number_id, Telegram bot username)
	ExternalAccountID string

	// Webhook wiring (bot / cloud APIs)
	PublicWebhookURL string
	WebhookSecret    string

	// Extras (region, business_account_id, proxy, …)
	Options map[string]any
	Timeout time.Duration
	Logger  any

	// OptionsUpdateFunc is a callback for protocols to persist updated options (e.g. QR codes)
	OptionsUpdateFunc func(map[string]any) error

	// OnPollConflict is fired by protocols when their long-poll surfaces a
	// persistent ownership conflict (e.g. Telegram getUpdates 409 from another
	// concurrent poller). The registry uses this signal to trigger a
	// reconcile against identity-service so it can drop orphaned providers.
	OnPollConflict func()
}

// Account is the identity a Provider authenticates as.
type Account struct {
	ID          string // Provider-side ID (bot_id, user_id)
	Platform    Platform
	AccountType AccountType
	Username    string // @handle
	DisplayName string
	Phone       string
	IsBot       bool
	RawMetadata map[string]any
}

// Messenger sends outbound messages. Every production-grade provider exposes
// this capability.
type Messenger interface {
	Provider
	// Send delivers a message. The implementation fills MessageID on success.
	Send(ctx context.Context, msg OutgoingMessage) (*SentMessage, error)
	// Edit updates a previously sent text message (where supported).
	Edit(ctx context.Context, chatID, messageID, newText string) (*SentMessage, error)
	// Delete removes a previously sent message (where supported).
	Delete(ctx context.Context, chatID, messageID string) error
}

// Receiver consumes inbound updates. Two idiomatic modes:
//
//   - Push: Listen() registers a handler and, for webhook-based providers,
//     is paired with HTTPHandler() on the Provider.
//   - Pull: Poll() drives long-polling providers (Telegram getUpdates,
//     WhatsApp on-premise /messages) for services that can't expose a
//     public webhook.
type Receiver interface {
	Provider
	Listen(ctx context.Context, handler UpdateHandler) error
	Poll(ctx context.Context, handler UpdateHandler) error
	// HTTPHandler is non-nil for webhook-based providers (Telegram webhook,
	// WA Cloud API). Services mount it on their public router.
	HTTPHandler() HTTPHandler
}

// GroupManager covers chat-membership operations for group-capable providers.
// Group hierarchies (Telegram supergroup → topic, WA community → group) are
// expressed by populating Chat.ParentID.
type GroupManager interface {
	Provider
	GetChat(ctx context.Context, chatID string) (*Chat, error)
	ListMembers(ctx context.Context, chatID string) ([]Account, error)
	AddMembers(ctx context.Context, chatID string, userIDs []string) error
	RemoveMember(ctx context.Context, chatID, userID string) error
	// ListSubChats returns chats whose ParentID == chatID (topics, sub-groups).
	ListSubChats(ctx context.Context, parentChatID string) ([]Chat, error)
}

// FileManager uploads / downloads media attachments.
type FileManager interface {
	Provider
	UploadFile(ctx context.Context, file FileUpload) (*FileRef, error)
	DownloadFile(ctx context.Context, fileID string) ([]byte, *FileRef, error)
}

// UpdateHandler receives inbound updates from a Receiver. Handlers must be
// safe for concurrent invocation.
type UpdateHandler func(ctx context.Context, update Update)

// HTTPHandler is the same shape as voip.core.HTTPHandler — a small wrapper so
// services can mount provider webhooks on their own router without importing
// provider-specific types.
type HTTPHandler interface {
	Path() string
	ServeHTTPRaw(ctx context.Context, req HTTPRequest) (HTTPResponse, error)
}

// HTTPRequest and HTTPResponse intentionally duplicate go-voip's types to
// avoid a cross-library import. They're trivial and rarely change.
type HTTPRequest struct {
	Method  string
	Path    string
	Headers map[string][]string
	Query   map[string][]string
	Body    []byte
}

type HTTPResponse struct {
	Status  int
	Headers map[string]string
	Body    []byte
}
