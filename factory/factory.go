// Package factory instantiates messenger providers from named presets.
// Mirrors the ergonomics of go-ai-providers/factory and go-voip/factory so
// services can persist all three provider kinds (AI, VOIP, messenger) with
// one shape in their config store.
package factory

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/madmike/go-messengers/core"
	"github.com/madmike/go-messengers/protocols/telegram_bot"
	"github.com/madmike/go-messengers/protocols/telegram_userbot"
	"github.com/madmike/go-messengers/protocols/whatsapp_cloud"
	"github.com/madmike/go-messengers/protocols/whatsapp_userbot"
)

// ProtocolFactory constructs a Provider from a ProviderConfig.
type ProtocolFactory func(config core.ProviderConfig) (core.Provider, error)

// ProtocolRegistry holds every available messenger protocol factory.
var ProtocolRegistry = map[string]ProtocolFactory{
	"telegram_bot":     telegram_bot.NewProtocol,
	"telegram_userbot": telegram_userbot.NewProtocol,
	"whatsapp_cloud":   whatsapp_cloud.NewProtocol,
	"whatsapp_userbot": whatsapp_userbot.NewProtocol,
}

// Preset is a named, pre-wired messenger provider configuration template.
type Preset struct {
	Protocol        string
	Platform        core.Platform
	AccountType     core.AccountType
	Name            string
	BaseURL         string
	RequiredOptions []string // keys that MUST appear in ProviderConfig.Options
	Description     string
}

// Presets contains predefined messenger provider configurations.
var Presets = map[string]Preset{
	"telegram_bot": {
		Protocol:    "telegram_bot",
		Platform:    core.PlatformTelegram,
		AccountType: core.AccountTypeBot,
		Name:        "Telegram Bot",
		BaseURL:     "https://api.telegram.org",
		Description: "Telegram Bot API (HTTP, webhook + getUpdates) — recommended default.",
	},
	"telegram_userbot": {
		Protocol:        "telegram_userbot",
		Platform:        core.PlatformTelegram,
		AccountType:     core.AccountTypeUserbot,
		Name:            "Telegram User Account (MTProto)",
		RequiredOptions: []string{"phone"},
		Description:     "Telegram user account driven via MTProto (gotd/td). Full group/channel access.",
	},
	"whatsapp_cloud": {
		Protocol:        "whatsapp_cloud",
		Platform:        core.PlatformWhatsApp,
		AccountType:     core.AccountTypeBusinessAPI,
		Name:            "WhatsApp Cloud API",
		BaseURL:         "https://graph.facebook.com",
		RequiredOptions: []string{"verify_token", "phone_number_id", "business_account_id"},
		Description:     "Meta Graph API v20 for WhatsApp Business — webhook-based, per-number access token.",
	},
	"whatsapp_userbot": {
		Protocol:        "whatsapp_userbot",
		Platform:        core.PlatformWhatsApp,
		AccountType:     core.AccountTypeUserbot,
		Name:            "WhatsApp User Account",
		RequiredOptions: []string{"phone"},
		Description:     "Pair-linked WhatsApp via whatsmeow. Full account surface.",
	},
}

// DBProviderConfig is what services persist to their tenant_providers table.
// Every messenger protocol reads only the fields it needs.
type DBProviderConfig struct {
	PresetName  string
	DisplayName string

	// Bot / Business API credentials.
	APIKey            string // Telegram bot token, WA permanent token
	APISecret         string // WA app secret (for signed webhooks)
	ExternalAccountID string // WA phone_number_id, Telegram bot username

	// Userbot credentials.
	AppID       int
	AppHash     string
	Phone       string
	SessionPath string

	BaseURL          string
	PublicWebhookURL string
	WebhookSecret    string
	Options          map[string]any
	Logger           any

	// OptionsUpdateFunc allows protocols to persist updated options (e.g., QR codes)
	OptionsUpdateFunc func(map[string]any) error

	// OnPollConflict is invoked by the protocol when a long-poll surfaces a
	// persistent ownership conflict (Telegram getUpdates 409). Callers can use
	// this to trigger a reconciliation against the source of truth.
	OnPollConflict func()
}

// CreateFromDB instantiates a messenger provider from DB-loaded config.
func CreateFromDB(cfg DBProviderConfig) (core.Provider, error) {
	preset, ok := Presets[cfg.PresetName]
	if !ok {
		return nil, fmt.Errorf("unknown messenger preset: %s", cfg.PresetName)
	}
	factory, ok := ProtocolRegistry[preset.Protocol]
	if !ok {
		return nil, fmt.Errorf("unknown messenger protocol: %s", preset.Protocol)
	}

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = preset.BaseURL
	}

	// For userbots, read appID, appHash, phone from options if not set directly
	appID := cfg.AppID
	appHash := cfg.AppHash
	phone := cfg.Phone
	sessionPath := cfg.SessionPath
	password2FA := ""

	if cfg.Options != nil {
		if appID == 0 {
			if v, ok := cfg.Options["app_id"]; ok {
				if f, ok := v.(float64); ok {
					appID = int(f)
				}
			}
		}
		if appHash == "" {
			if v, ok := cfg.Options["app_hash"]; ok {
				if s, ok := v.(string); ok {
					appHash = s
				}
			}
		}
		if phone == "" {
			if v, ok := cfg.Options["phone"]; ok {
				if s, ok := v.(string); ok {
					phone = s
				}
			}
		}
		if sessionPath == "" {
			if v, ok := cfg.Options["session_path"]; ok {
				if s, ok := v.(string); ok {
					sessionPath = s
				}
			}
		}
		if v, ok := cfg.Options["password_2fa"]; ok {
			if s, ok := v.(string); ok {
				password2FA = s
			}
		}
	}


	// For telegram_userbot, fall back to environment variables for app_id and app_hash
	if cfg.PresetName == "telegram_userbot" {
		if appID == 0 {
			if envVal := os.Getenv("TELEGRAM_USERBOT_APP_ID"); envVal != "" {
				if parsed, err := strconv.Atoi(envVal); err == nil {
					appID = parsed
				}
			}
		}
		if appHash == "" {
			if envVal := os.Getenv("TELEGRAM_USERBOT_APP_HASH"); envVal != "" {
				appHash = envVal
			}
		}
	}

	providerCfg := core.ProviderConfig{
		Name:              cfg.DisplayName,
		Platform:          preset.Platform,
		AccountType:       preset.AccountType,
		APIKey:            cfg.APIKey,
		APISecret:         cfg.APISecret,
		BaseURL:           baseURL,
		AppID:             appID,
		AppHash:           appHash,
		Phone:             phone,
		SessionPath:       sessionPath,
		Password2FA:       password2FA,
		ExternalAccountID: cfg.ExternalAccountID,

		PublicWebhookURL:  cfg.PublicWebhookURL,
		WebhookSecret:     cfg.WebhookSecret,
		Options:           cfg.Options,
		Logger:            cfg.Logger,
		OptionsUpdateFunc: cfg.OptionsUpdateFunc,
		OnPollConflict:    cfg.OnPollConflict,
	}

	for _, key := range preset.RequiredOptions {
		if providerCfg.Options == nil || providerCfg.Options[key] == nil {
			return nil, fmt.Errorf("required option %q missing for preset %s", key, cfg.PresetName)
		}
	}

	provider, err := factory(providerCfg)
	if err != nil {
		return nil, err
	}
	if err := provider.Initialize(context.Background(), providerCfg); err != nil {
		return nil, fmt.Errorf("initialize %s: %w", cfg.DisplayName, err)
	}
	return provider, nil
}

// GetPreset returns a preset by name.
func GetPreset(name string) (Preset, bool) {
	p, ok := Presets[name]
	return p, ok
}

// ListPresets returns all known messenger presets.
func ListPresets() map[string]Preset { return Presets }
