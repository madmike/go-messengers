// Package whatsapp_cloud implements core.Provider for WhatsApp Cloud API
// (Meta Graph API v20+).
//
// Reference: https://developers.facebook.com/docs/whatsapp/cloud-api
//
// Accounts are identified by a "phone_number_id" (the WA Cloud API's unit of
// addressability) and driven by a permanent access token. Inbound messages
// arrive only via webhooks — there's no long-polling equivalent.
package whatsapp_cloud

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/madmike/go-infra/telemetry"
	"github.com/madmike/go-messengers/core"
)

// DefaultGraphVersion is the Graph API version used when none is configured.
const DefaultGraphVersion = "v20.0"

type Protocol struct {
	name          string
	token         string
	phoneNumberID string // WA Cloud API "from" id for outbound
	businessID    string // WhatsApp Business Account id (WABA)
	baseURL       string // e.g. https://graph.facebook.com
	version       string // Graph API version, e.g. v20.0
	webhookURL    string
	verifyToken   string // for Meta's GET verification handshake
	appSecret     string // for X-Hub-Signature-256 validation
	logger        telemetry.Logger
	httpClient    *http.Client

	mu          sync.RWMutex
	initialized bool
	me          *core.Account
	handler     core.UpdateHandler
}

func NewProtocol(config core.ProviderConfig) (core.Provider, error) {
	timeout := config.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	version := DefaultGraphVersion
	if v, _ := config.Options["graph_version"].(string); v != "" {
		version = v
	}
	businessID, _ := config.Options["business_account_id"].(string)
	verifyToken, _ := config.Options["verify_token"].(string)

	p := &Protocol{
		name:          config.Name,
		token:         config.APIKey,
		phoneNumberID: config.ExternalAccountID,
		businessID:    businessID,
		baseURL:       firstNonEmpty(config.BaseURL, "https://graph.facebook.com"),
		version:       version,
		webhookURL:    config.PublicWebhookURL,
		verifyToken:   verifyToken,
		appSecret:     config.APISecret,
		logger:        resolveLogger(config.Logger),
		httpClient:    &http.Client{Timeout: timeout},
	}
	return p, nil
}

func (p *Protocol) Name() string                  { return p.name }
func (p *Protocol) Platform() core.Platform       { return core.PlatformWhatsApp }
func (p *Protocol) AccountType() core.AccountType { return core.AccountTypeBusinessAPI }

func (p *Protocol) Capabilities() []core.Capability {
	return []core.Capability{
		core.CapabilitySend,
		core.CapabilityReceive,
		core.CapabilityFiles,
		core.CapabilityReactions,
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
		return fmt.Errorf("whatsapp_cloud: access token required")
	}
	if config.ExternalAccountID == "" {
		return fmt.Errorf("whatsapp_cloud: phone_number_id (ExternalAccountID) required")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.token = config.APIKey
	p.phoneNumberID = config.ExternalAccountID
	if config.BaseURL != "" {
		p.baseURL = config.BaseURL
	}
	if config.APISecret != "" {
		p.appSecret = config.APISecret
	}
	if v, _ := config.Options["graph_version"].(string); v != "" {
		p.version = v
	}
	if vt, _ := config.Options["verify_token"].(string); vt != "" {
		p.verifyToken = vt
	}
	if bid, _ := config.Options["business_account_id"].(string); bid != "" {
		p.businessID = bid
	}
	p.initialized = true
	return nil
}

func (p *Protocol) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initialized = false
	return nil
}

func (p *Protocol) HealthCheck(ctx context.Context) error {
	p.mu.RLock()
	ok := p.initialized
	p.mu.RUnlock()
	if !ok {
		return fmt.Errorf("whatsapp_cloud: not initialized")
	}
	_, err := p.Me(ctx)
	return err
}

// Me returns the phone-number identity via GET /{phone_number_id}.
func (p *Protocol) Me(ctx context.Context) (*core.Account, error) {
	p.mu.RLock()
	cached := p.me
	p.mu.RUnlock()
	if cached != nil {
		return cached, nil
	}
	var resp struct {
		ID                 string `json:"id"`
		DisplayPhoneNumber string `json:"display_phone_number"`
		VerifiedName       string `json:"verified_name"`
	}
	if err := p.graphGet(ctx, p.phoneNumberID, nil, &resp); err != nil {
		return nil, err
	}
	acc := &core.Account{
		ID:          resp.ID,
		Platform:    core.PlatformWhatsApp,
		AccountType: core.AccountTypeBusinessAPI,
		DisplayName: resp.VerifiedName,
		Phone:       resp.DisplayPhoneNumber,
	}
	p.mu.Lock()
	p.me = acc
	p.mu.Unlock()
	return acc, nil
}

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
