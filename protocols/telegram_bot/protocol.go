// Package telegram_bot implements core.Provider for the Telegram Bot API.
//
// Reference: https://core.telegram.org/bots/api
//
// Supports webhook (push) and getUpdates (long-polling) modes. Media upload
// goes through the "multipart/form-data" path; download resolves file_path
// via getFile and streams the CDN URL.
package telegram_bot

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/madmike/go-infra/telemetry"
	"github.com/madmike/go-messengers/core"
)

type Protocol struct {
	name           string
	token          string
	baseURL        string
	webhookURL     string
	secretTok      string
	logger         telemetry.Logger
	httpClient     *http.Client
	onPollConflict func()

	mu          sync.RWMutex
	initialized bool
	me          *core.Account

	// Receiver state
	handler    core.UpdateHandler
	pollCancel context.CancelFunc
	pollDone   chan struct{}
}

// NewProtocol is the factory entrypoint.
func NewProtocol(config core.ProviderConfig) (core.Provider, error) {
	timeout := config.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	p := &Protocol{
		name:           config.Name,
		token:          config.APIKey,
		baseURL:        firstNonEmpty(config.BaseURL, "https://api.telegram.org"),
		webhookURL:     config.PublicWebhookURL,
		secretTok:      config.WebhookSecret,
		logger:         resolveLogger(config.Logger),
		httpClient:     &http.Client{Timeout: timeout},
		onPollConflict: config.OnPollConflict,
	}
	return p, nil
}

func (p *Protocol) Name() string                  { return p.name }
func (p *Protocol) Platform() core.Platform       { return core.PlatformTelegram }
func (p *Protocol) AccountType() core.AccountType { return core.AccountTypeBot }

func (p *Protocol) Capabilities() []core.Capability {
	return []core.Capability{
		core.CapabilitySend,
		core.CapabilityReceive,
		core.CapabilityGroups,
		core.CapabilityFiles,
		core.CapabilityReactions,
		core.CapabilityEditDelete,
	}
}

func (p *Protocol) SupportsCapability(c core.Capability) bool {
	for _, have := range p.Capabilities() {
		if have == c {
			return true
		}
	}
	return false
}

func (p *Protocol) Initialize(ctx context.Context, config core.ProviderConfig) error {
	if config.APIKey == "" {
		return fmt.Errorf("telegram_bot: bot token required")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.token = config.APIKey
	if config.BaseURL != "" {
		p.baseURL = config.BaseURL
	}
	if config.PublicWebhookURL != "" {
		p.webhookURL = config.PublicWebhookURL
	}
	if config.WebhookSecret != "" {
		p.secretTok = config.WebhookSecret
	}
	p.initialized = true
	return nil
}

func (p *Protocol) Close() error {
	p.mu.Lock()
	if p.pollCancel != nil {
		p.pollCancel()
		p.pollCancel = nil
	}
	p.mu.Unlock()
	p.initialized = false
	return nil
}

func (p *Protocol) HealthCheck(ctx context.Context) error {
	p.mu.RLock()
	ok := p.initialized
	p.mu.RUnlock()
	if !ok {
		return fmt.Errorf("telegram_bot: not initialized")
	}
	_, err := p.Me(ctx)
	return err
}

// Me returns the bot identity via getMe. Cached after first success.
func (p *Protocol) Me(ctx context.Context) (*core.Account, error) {
	p.mu.RLock()
	cached := p.me
	p.mu.RUnlock()
	if cached != nil {
		return cached, nil
	}
	var resp struct {
		ID        int64  `json:"id"`
		IsBot     bool   `json:"is_bot"`
		FirstName string `json:"first_name"`
		Username  string `json:"username"`
	}
	if err := p.apiCall(ctx, "getMe", nil, &resp); err != nil {
		return nil, err
	}
	acc := &core.Account{
		ID:          fmt.Sprintf("%d", resp.ID),
		Platform:    core.PlatformTelegram,
		AccountType: core.AccountTypeBot,
		Username:    resp.Username,
		DisplayName: resp.FirstName,
		IsBot:       resp.IsBot,
	}
	p.mu.Lock()
	p.me = acc
	p.mu.Unlock()
	return acc, nil
}

// HTTPHandler satisfies core.Receiver (see receive.go). Exposed on the
// Provider for factory consumers that type-assert to core.Receiver.
func (p *Protocol) HTTPHandler() core.HTTPHandler {
	return &webhookHandler{protocol: p}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func resolveLogger(l any) telemetry.Logger {
	if l == nil {
		return &telemetry.NoOpLogger{}
	}
	if logger, ok := l.(telemetry.Logger); ok {
		return logger
	}
	return &telemetry.NoOpLogger{}
}
