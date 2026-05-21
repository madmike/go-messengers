package core

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// MockProvider for testing.
type MockProvider struct {
	name       string
	platform   Platform
	accountType AccountType
	caps       []Capability
	account    *Account
	err        error
}

func NewMockProvider(name string, platform Platform) *MockProvider {
	return &MockProvider{
		name:     name,
		platform: platform,
		accountType: AccountTypeBot,
		caps:     []Capability{CapabilitySend, CapabilityReceive},
		account: &Account{
			ID:       "test-id",
			Platform: platform,
			Username: "testbot",
		},
	}
}

func (m *MockProvider) Name() string { return m.name }
func (m *MockProvider) Platform() Platform { return m.platform }
func (m *MockProvider) AccountType() AccountType { return m.accountType }
func (m *MockProvider) Initialize(ctx context.Context, config ProviderConfig) error { return m.err }
func (m *MockProvider) Close() error { return nil }
func (m *MockProvider) HealthCheck(ctx context.Context) error { return m.err }
func (m *MockProvider) Capabilities() []Capability { return m.caps }
func (m *MockProvider) SupportsCapability(cap Capability) bool {
	for _, c := range m.caps {
		if c == cap {
			return true
		}
	}
	return false
}
func (m *MockProvider) Me(ctx context.Context) (*Account, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.account, nil
}

func (m *MockProvider) SetCapabilities(caps []Capability) {
	m.caps = caps
}

func (m *MockProvider) SetError(err error) {
	m.err = err
}

// TestProviderIdentity returns name and platform.
func TestProviderIdentity(t *testing.T) {
	provider := NewMockProvider("Telegram Bot", PlatformTelegram)

	require.Equal(t, "Telegram Bot", provider.Name())
	require.Equal(t, PlatformTelegram, provider.Platform())
	require.Equal(t, AccountTypeBot, provider.AccountType())
}

// TestProviderCapabilities lists supported operations.
func TestProviderCapabilities(t *testing.T) {
	provider := NewMockProvider("Test", PlatformTelegram)
	provider.SetCapabilities([]Capability{CapabilitySend, CapabilityReceive, CapabilityGroups})

	caps := provider.Capabilities()
	require.Equal(t, 3, len(caps))
	require.Contains(t, caps, CapabilitySend)
	require.Contains(t, caps, CapabilityGroups)
}

// TestProviderSupportsCapability checks individual capability.
func TestProviderSupportsCapability(t *testing.T) {
	provider := NewMockProvider("Test", PlatformTelegram)
	provider.SetCapabilities([]Capability{CapabilitySend, CapabilityReceive})

	require.True(t, provider.SupportsCapability(CapabilitySend))
	require.True(t, provider.SupportsCapability(CapabilityReceive))
	require.False(t, provider.SupportsCapability(CapabilityGroups))
}

// TestProviderMe returns authenticated identity.
func TestProviderMe(t *testing.T) {
	provider := NewMockProvider("Test", PlatformTelegram)

	account, err := provider.Me(context.Background())
	require.NoError(t, err)
	require.NotNil(t, account)
	require.Equal(t, "test-id", account.ID)
	require.Equal(t, "testbot", account.Username)
}

// TestProviderHealthCheck verifies connectivity.
func TestProviderHealthCheck(t *testing.T) {
	provider := NewMockProvider("Test", PlatformTelegram)

	err := provider.HealthCheck(context.Background())
	require.NoError(t, err)
}

// TestProviderInitialize configures provider.
func TestProviderInitialize(t *testing.T) {
	provider := NewMockProvider("Test", PlatformTelegram)
	cfg := ProviderConfig{
		Name:    "Telegram Bot",
		Platform: PlatformTelegram,
		APIKey:  "token-123",
	}

	err := provider.Initialize(context.Background(), cfg)
	require.NoError(t, err)
}

// TestPlatformEnums validates constant values.
func TestPlatformEnums(t *testing.T) {
	require.Equal(t, Platform("telegram"), PlatformTelegram)
	require.Equal(t, Platform("whatsapp"), PlatformWhatsApp)
	require.Equal(t, Platform("viber"), PlatformViber)
	require.Equal(t, Platform("vk"), PlatformVK)
	require.Equal(t, Platform("facebook_messenger"), PlatformFacebookMessenger)
	require.Equal(t, Platform("instagram"), PlatformInstagram)
}

// TestAccountTypeEnums validates constant values.
func TestAccountTypeEnums(t *testing.T) {
	require.Equal(t, AccountType("bot"), AccountTypeBot)
	require.Equal(t, AccountType("business_api"), AccountTypeBusinessAPI)
	require.Equal(t, AccountType("userbot"), AccountTypeUserbot)
}

// TestCapabilityEnums validates constant values.
func TestCapabilityEnums(t *testing.T) {
	require.Equal(t, Capability("send"), CapabilitySend)
	require.Equal(t, Capability("receive"), CapabilityReceive)
	require.Equal(t, Capability("groups"), CapabilityGroups)
	require.Equal(t, Capability("files"), CapabilityFiles)
	require.Equal(t, Capability("reactions"), CapabilityReactions)
	require.Equal(t, Capability("presence"), CapabilityPresence)
	require.Equal(t, Capability("edit_delete"), CapabilityEditDelete)
}

// TestProviderConfigStructure includes all fields.
func TestProviderConfigStructure(t *testing.T) {
	cfg := ProviderConfig{
		Name:              "Test Provider",
		Platform:          PlatformTelegram,
		AccountType:       AccountTypeBot,
		APIKey:            "key-123",
		APISecret:         "secret-456",
		AppID:             123456,
		AppHash:           "hash-abc",
		Phone:             "+1234567890",
		SessionPath:       "/tmp/session",
		ExternalAccountID: "ext-id",
		PublicWebhookURL:  "https://example.com/webhook",
		WebhookSecret:     "webhook-secret",
		Options:           map[string]any{"option": "value"},
	}

	require.Equal(t, "Test Provider", cfg.Name)
	require.Equal(t, PlatformTelegram, cfg.Platform)
	require.Equal(t, "key-123", cfg.APIKey)
}

// TestAccountStructure includes all fields.
func TestAccountStructure(t *testing.T) {
	account := &Account{
		ID:          "user-123",
		Platform:    PlatformWhatsApp,
		AccountType: AccountTypeUserbot,
		Username:    "@user",
		DisplayName: "John Doe",
		Phone:       "+1234567890",
		IsBot:       false,
		RawMetadata: map[string]any{"field": "value"},
	}

	require.Equal(t, "user-123", account.ID)
	require.Equal(t, "@user", account.Username)
	require.False(t, account.IsBot)
}

// TestMultiplePlatforms distinguishes different networks.
func TestMultiplePlatforms(t *testing.T) {
	providers := []Provider{
		NewMockProvider("TG Bot", PlatformTelegram),
		NewMockProvider("WA Cloud", PlatformWhatsApp),
		NewMockProvider("FB Messenger", PlatformFacebookMessenger),
	}

	require.Equal(t, PlatformTelegram, providers[0].Platform())
	require.Equal(t, PlatformWhatsApp, providers[1].Platform())
	require.Equal(t, PlatformFacebookMessenger, providers[2].Platform())
}

// TestProviderErrorHandling propagates errors.
func TestProviderErrorHandling(t *testing.T) {
	provider := NewMockProvider("Test", PlatformTelegram)
	provider.SetError(ErrProviderError())

	err := provider.HealthCheck(context.Background())
	require.Error(t, err)

	account, err := provider.Me(context.Background())
	require.Error(t, err)
	require.Nil(t, account)
}

// TestProviderClosing cleans up resources.
func TestProviderClosing(t *testing.T) {
	provider := NewMockProvider("Test", PlatformTelegram)

	err := provider.Close()
	require.NoError(t, err)
}

// TestCapabilityMatrixForProviders tests capability combinations.
func TestCapabilityMatrixForProviders(t *testing.T) {
	cases := []struct {
		name string
		caps []Capability
	}{
		{"Bot (send/receive)", []Capability{CapabilitySend, CapabilityReceive}},
		{"Userbot (all)", []Capability{CapabilitySend, CapabilityReceive, CapabilityGroups, CapabilityFiles, CapabilityEditDelete}},
		{"Minimal", []Capability{CapabilitySend}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := NewMockProvider(tc.name, PlatformTelegram)
			provider.SetCapabilities(tc.caps)

			for _, cap := range tc.caps {
				require.True(t, provider.SupportsCapability(cap))
			}
		})
	}
}

// TestProviderWithWebhook configures webhook.
func TestProviderWithWebhook(t *testing.T) {
	cfg := ProviderConfig{
		PublicWebhookURL: "https://api.example.com/webhook/telegram",
		WebhookSecret:    "secret-key",
	}

	require.Equal(t, "https://api.example.com/webhook/telegram", cfg.PublicWebhookURL)
	require.Equal(t, "secret-key", cfg.WebhookSecret)
}

func ErrProviderError() error {
	return &ProviderError{}
}

type ProviderError struct{}

func (e *ProviderError) Error() string {
	return "provider error"
}
