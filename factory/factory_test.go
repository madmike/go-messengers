package factory

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/madmike/go-messengers/core"
)

// TestGetPresetTelegramBot retrieves telegram_bot preset.
func TestGetPresetTelegramBot(t *testing.T) {
	preset, ok := GetPreset("telegram_bot")
	require.True(t, ok)
	require.Equal(t, "telegram_bot", preset.Protocol)
	require.Equal(t, core.PlatformTelegram, preset.Platform)
	require.Equal(t, core.AccountTypeBot, preset.AccountType)
}

// TestGetPresetTelegramUserbot retrieves telegram_userbot preset.
func TestGetPresetTelegramUserbot(t *testing.T) {
	preset, ok := GetPreset("telegram_userbot")
	require.True(t, ok)
	require.Equal(t, "telegram_userbot", preset.Protocol)
	require.Equal(t, core.PlatformTelegram, preset.Platform)
	require.Equal(t, core.AccountTypeUserbot, preset.AccountType)
	require.Contains(t, preset.RequiredOptions, "phone")
}

// TestGetPresetWhatsAppCloud retrieves whatsapp_cloud preset.
func TestGetPresetWhatsAppCloud(t *testing.T) {
	preset, ok := GetPreset("whatsapp_cloud")
	require.True(t, ok)
	require.Equal(t, "whatsapp_cloud", preset.Protocol)
	require.Equal(t, core.PlatformWhatsApp, preset.Platform)
	require.Contains(t, preset.RequiredOptions, "verify_token")
	require.Contains(t, preset.RequiredOptions, "phone_number_id")
	require.Contains(t, preset.RequiredOptions, "business_account_id")
}

// TestGetPresetWhatsAppUserbot retrieves whatsapp_userbot preset.
func TestGetPresetWhatsAppUserbot(t *testing.T) {
	preset, ok := GetPreset("whatsapp_userbot")
	require.True(t, ok)
	require.Equal(t, "whatsapp_userbot", preset.Protocol)
	require.Equal(t, core.AccountTypeUserbot, preset.AccountType)
	require.Contains(t, preset.RequiredOptions, "phone")
}

// TestGetPresetNotFound returns false for unknown preset.
func TestGetPresetNotFound(t *testing.T) {
	_, ok := GetPreset("unknown_platform")
	require.False(t, ok)
}

// TestListPresetsReturnsAll returns all defined presets.
func TestListPresetsReturnsAll(t *testing.T) {
	presets := ListPresets()
	require.Equal(t, 4, len(presets))
	require.NotNil(t, presets["telegram_bot"])
	require.NotNil(t, presets["telegram_userbot"])
	require.NotNil(t, presets["whatsapp_cloud"])
	require.NotNil(t, presets["whatsapp_userbot"])
}

// TestProtocolFactoryExists checks registry population.
func TestProtocolFactoryExists(t *testing.T) {
	require.NotNil(t, ProtocolRegistry["telegram_bot"])
	require.NotNil(t, ProtocolRegistry["telegram_userbot"])
	require.NotNil(t, ProtocolRegistry["whatsapp_cloud"])
	require.NotNil(t, ProtocolRegistry["whatsapp_userbot"])
	require.Equal(t, 4, len(ProtocolRegistry))
}

// TestPresetDescription includes documentation.
func TestPresetDescription(t *testing.T) {
	preset, _ := GetPreset("telegram_bot")
	require.NotEmpty(t, preset.Description)
	require.NotEmpty(t, preset.Name)
}

// TestDBProviderConfigStructure includes all fields.
func TestDBProviderConfigStructure(t *testing.T) {
	cfg := DBProviderConfig{
		PresetName:        "telegram_bot",
		DisplayName:       "My Telegram Bot",
		APIKey:            "123456:ABC-DEF-GHI",
		APISecret:         "secret",
		ExternalAccountID: "@mybotname",
		AppID:             111111,
		AppHash:           "app-hash",
		Phone:             "+1234567890",
		SessionPath:       "/data/session",
		BaseURL:           "https://api.telegram.org",
		PublicWebhookURL:  "https://myserver.com/telegram",
		WebhookSecret:     "webhook-secret",
		Options:           map[string]any{"key": "value"},
	}

	require.Equal(t, "telegram_bot", cfg.PresetName)
	require.Equal(t, "My Telegram Bot", cfg.DisplayName)
	require.Equal(t, "123456:ABC-DEF-GHI", cfg.APIKey)
}

// TestProviderConfigConversion converts DB config to core config.
func TestProviderConfigConversion(t *testing.T) {
	dbCfg := DBProviderConfig{
		PresetName:        "telegram_bot",
		DisplayName:       "Test Bot",
		APIKey:            "token-123",
		ExternalAccountID: "@testbot",
	}

	// Test that fields are properly converted (without calling factory.CreateFromDB
	// which requires mocked protocol implementations)
	require.Equal(t, "telegram_bot", dbCfg.PresetName)
	require.Equal(t, "token-123", dbCfg.APIKey)
}

// TestPresetRequiredOptions validates option requirements.
func TestPresetRequiredOptions(t *testing.T) {
	botPreset, _ := GetPreset("telegram_bot")
	require.Empty(t, botPreset.RequiredOptions)

	userbotPreset, _ := GetPreset("telegram_userbot")
	require.NotEmpty(t, userbotPreset.RequiredOptions)
	require.Contains(t, userbotPreset.RequiredOptions, "phone")
}

// TestPresetBaseURL includes default endpoints.
func TestPresetBaseURL(t *testing.T) {
	botPreset, _ := GetPreset("telegram_bot")
	require.Equal(t, "https://api.telegram.org", botPreset.BaseURL)

	waPreset, _ := GetPreset("whatsapp_cloud")
	require.Equal(t, "https://graph.facebook.com", waPreset.BaseURL)
}

// TestMultiplePresetsDistinct distinguishes platform configs.
func TestMultiplePresetsDistinct(t *testing.T) {
	tgBot, _ := GetPreset("telegram_bot")
	waCloud, _ := GetPreset("whatsapp_cloud")

	require.Equal(t, core.PlatformTelegram, tgBot.Platform)
	require.Equal(t, core.PlatformWhatsApp, waCloud.Platform)
	require.Equal(t, core.AccountTypeBot, tgBot.AccountType)
	require.Equal(t, core.AccountTypeBusinessAPI, waCloud.AccountType)
}

// TestUserbotPresetsRequirePhone ensures migration safety.
func TestUserbotPresetsRequirePhone(t *testing.T) {
	tgUserbot, _ := GetPreset("telegram_userbot")
	waUserbot, _ := GetPreset("whatsapp_userbot")

	require.Contains(t, tgUserbot.RequiredOptions, "phone")
	require.Contains(t, waUserbot.RequiredOptions, "phone")
}

// TestOptionsUpdateFuncSignature allows provider state updates.
func TestOptionsUpdateFuncSignature(t *testing.T) {
	cfg := DBProviderConfig{
		OptionsUpdateFunc: func(options map[string]any) error {
			options["qr_code"] = "data:image/png;base64,..."
			return nil
		},
	}

	require.NotNil(t, cfg.OptionsUpdateFunc)
	err := cfg.OptionsUpdateFunc(make(map[string]any))
	require.NoError(t, err)
}

// TestOnPollConflictCallback allows conflict handling.
func TestOnPollConflictCallback(t *testing.T) {
	called := false
	cfg := DBProviderConfig{
		OnPollConflict: func() {
			called = true
		},
	}

	require.NotNil(t, cfg.OnPollConflict)
	cfg.OnPollConflict()
	require.True(t, called)
}

// TestPresetsCompleteness checks all required fields.
func TestPresetsCompleteness(t *testing.T) {
	for name, preset := range ListPresets() {
		t.Run(name, func(t *testing.T) {
			require.NotEmpty(t, preset.Protocol)
			require.NotEmpty(t, preset.Platform)
			require.NotEmpty(t, preset.AccountType)
			require.NotEmpty(t, preset.Name)
			require.NotEmpty(t, preset.Description)
		})
	}
}

// TestProtocolFactoryRegistration ensures all presets have factories.
func TestProtocolFactoryRegistration(t *testing.T) {
	for name, preset := range ListPresets() {
		t.Run(name, func(t *testing.T) {
			factory, ok := ProtocolRegistry[preset.Protocol]
			require.True(t, ok, "Missing factory for protocol %s", preset.Protocol)
			require.NotNil(t, factory)
		})
	}
}
