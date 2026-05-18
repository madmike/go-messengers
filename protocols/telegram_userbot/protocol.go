// Package telegram_userbot drives Telegram user accounts via gotd/td MTProto.
// QR-code-based login for authenticated user accounts.
//
// Compliance notes:
// - Single AppID/AppHash shared across all user accounts (Telegram-approved pattern)
// - Each user's session persisted separately to prevent re-auth on restart
// - Device info varies per user to avoid "identical device" detection
// - Used for message relay only, not data scraping
package telegram_userbot

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"

	"github.com/gotd/td/tg"
	"github.com/madmike/go-infra/telemetry"
	"github.com/madmike/go-messengers/core"
)

type Protocol struct {
	name    string
	appID   int
	appHash string
	phone   string
	password2FA string
	logger  telemetry.Logger


	mu                sync.RWMutex
	handler           core.UpdateHandler
	client            *telegram.Client
	dispatcher        tg.UpdateDispatcher
	sessionStorage    *MemorySessionStorage
	hadUserSession    bool // True if we loaded existing user session data during init (vs. fresh start)
	accountID         string
	optionsUpdateFunc func(map[string]any) error
	passwordReadyChan chan string
	qrRefreshChan     chan struct{}
}

func NewProtocol(config core.ProviderConfig) (core.Provider, error) {
	if config.AppID == 0 || config.AppHash == "" {
		return nil, fmt.Errorf("telegram_userbot: AppID and AppHash required")
	}
	if config.Phone == "" {
		return nil, fmt.Errorf("telegram_userbot: Phone (E.164) required")
	}
	logger, _ := config.Logger.(telemetry.Logger)
	if logger == nil {
		logger = &telemetry.NoOpLogger{}
	}
	return &Protocol{
		name:              config.Name,
		appID:             config.AppID,
		appHash:           config.AppHash,
		phone:             config.Phone,
		password2FA:       config.Password2FA,
		logger:            logger,
		passwordReadyChan: make(chan string, 1),
		qrRefreshChan:     make(chan struct{}, 1),
	}, nil

}

func (p *Protocol) Name() string                  { return p.name }
func (p *Protocol) Platform() core.Platform       { return core.PlatformTelegram }
func (p *Protocol) AccountType() core.AccountType { return core.AccountTypeUserbot }

func (p *Protocol) Capabilities() []core.Capability {
	return []core.Capability{
		core.CapabilitySend,
		core.CapabilityReceive,
		core.CapabilityGroups,
		core.CapabilityFiles,
		core.CapabilityReactions,
		core.CapabilityPresence,
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

// Initialize validates config and pre-builds the client. No network calls happen here.
func (p *Protocol) Initialize(ctx context.Context, config core.ProviderConfig) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if config.Options != nil {
		if id, ok := config.Options["account_id"]; ok {
			p.accountID = fmt.Sprintf("%v", id)
		}
	}

	if config.OptionsUpdateFunc != nil {
		p.optionsUpdateFunc = config.OptionsUpdateFunc
	}

	deviceHash := fmt.Sprintf("%x", md5.Sum([]byte(p.accountID)))[:8]

	// Load previous session from options if available (database-backed)
	sessionStorage := &MemorySessionStorage{}

	// Check if force_clear_session flag is set (indicates a reactivation attempt)
	forceClear := false
	if config.Options != nil {
		if fc, ok := config.Options["force_clear_session"]; ok {
			if b, ok := fc.(bool); ok {
				forceClear = b
			}
		}
	}

	hadUserSession := false
	if forceClear {
		p.logger.Info("telegram_userbot: force_clear_session enabled, skipping cached session",
			telemetry.String("phone", p.phone))
	} else if config.Options != nil {
		if raw, ok := config.Options["session_data_b64"]; ok {
			if b64Str, ok := raw.(string); ok {
				codec := &SessionCodec{}
				if err := codec.Decode(b64Str); err != nil {
					p.logger.Info("telegram_userbot: could not decode previous session, starting fresh",
						telemetry.Err(err))
				} else {
					sessionStorage.data = codec.Data()
					hadUserSession = len(sessionStorage.data) > 0
				}
			}
		}
	}
	p.hadUserSession = hadUserSession

	dispatcher := tg.NewUpdateDispatcher()

	client := telegram.NewClient(p.appID, p.appHash, telegram.Options{
		SessionStorage: sessionStorage,
		UpdateHandler:  dispatcher,
		Device: telegram.DeviceConfig{
			DeviceModel:    fmt.Sprintf("Aulinq Bot %s", deviceHash),
			SystemVersion:  "Linux",
			SystemLangCode: "en",
		},
	})

	p.sessionStorage = sessionStorage
	p.dispatcher = dispatcher
	p.client = client

	p.logger.Info("telegram_userbot: initialized",
		telemetry.String("phone", p.phone),
		telemetry.String("account_id", p.accountID),
		telemetry.Bool("has_session", len(sessionStorage.data) > 0))
	return nil
}

func (p *Protocol) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.client = nil
	return nil
}

// Disconnect closes the Telegram connection and clears the session.
func (p *Protocol) Disconnect(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.client == nil {
		return fmt.Errorf("telegram_userbot: not initialized")
	}

	// Clear the session storage to force re-authentication on next connect
	if p.sessionStorage != nil {
		p.sessionStorage.data = nil
	}

	// Nil out the client to stop the connection
	p.client = nil
	p.logger.Info("telegram_userbot: device disconnected",
		telemetry.String("account_id", p.accountID))

	return nil
}

func (p *Protocol) SubmitPassword(password string) error {
	if password == "" {
		return fmt.Errorf("password cannot be empty")
	}
	select {
	case p.passwordReadyChan <- password:
		return nil
	default:
		return fmt.Errorf("unable to submit password (channel full or not waiting)")
	}
}

func (p *Protocol) RefreshQR() error {
	select {
	case p.qrRefreshChan <- struct{}{}:
		return nil
	default:
		return fmt.Errorf("qr refresh already in progress")
	}
}

func (p *Protocol) HealthCheck(ctx context.Context) error {
	p.mu.RLock()
	c := p.client
	p.mu.RUnlock()
	if c == nil {
		return fmt.Errorf("telegram_userbot: not initialized")
	}
	return nil
}

func (p *Protocol) Me(ctx context.Context) (*core.Account, error) {
	p.mu.RLock()
	c := p.client
	p.mu.RUnlock()
	if c == nil {
		return nil, fmt.Errorf("telegram_userbot: not initialized")
	}
	self, err := c.Self(ctx)
	if err != nil {
		return nil, fmt.Errorf("get self failed: %w", err)
	}
	return &core.Account{
		ID:          fmt.Sprintf("%d", self.ID),
		Platform:    core.PlatformTelegram,
		AccountType: core.AccountTypeUserbot,
		Phone:       p.phone,
		IsBot:       false,
	}, nil
}

// Listen and Poll both drive the MTProto connection — for MTProto there is only
// one connection mode. Both methods block until ctx is cancelled.
func (p *Protocol) Listen(ctx context.Context, handler core.UpdateHandler) error {
	if handler == nil {
		return fmt.Errorf("telegram_userbot: nil handler")
	}
	return p.run(ctx, handler)
}

func (p *Protocol) Poll(ctx context.Context, handler core.UpdateHandler) error {
	return p.Listen(ctx, handler)
}

func (p *Protocol) HTTPHandler() core.HTTPHandler { return nil }

// run establishes the MTProto connection and drives the QR auth flow if needed.
// Blocks until ctx is cancelled.
func (p *Protocol) run(ctx context.Context, handler core.UpdateHandler) error {
	p.mu.Lock()
	p.handler = handler
	// Register for UpdateLoginToken BEFORE client.Run so we don't miss the first signal.
	loggedIn := qrlogin.OnLoginToken(p.dispatcher)
	p.mu.Unlock()

	p.mu.RLock()
	client := p.client
	p.mu.RUnlock()
	if client == nil {
		return fmt.Errorf("telegram_userbot: Initialize not called")
	}

	p.logger.Info("telegram_userbot: starting MTProto connection",
		telemetry.String("phone", p.phone))

	err := client.Run(ctx, func(ctx context.Context) error {
		// Use hadUserSession (set in Initialize before MTProto auth-key bytes are written).
		// We cannot rely on sessionStorage.data here because the gotd client writes auth-key
		// bytes to the storage as part of the MTProto handshake, even without user auth.
		p.mu.RLock()
		hadUserSession := p.hadUserSession
		p.mu.RUnlock()

		// 1. If we had user session data on init, probe whether it's still valid.
		sessionValid := false
		if hadUserSession {
			if _, err := client.Self(ctx); err == nil {
				p.logger.Info("telegram_userbot: session restored, skipping QR auth",
					telemetry.String("phone", p.phone))
				sessionValid = true
			} else if isAuthError(err) {
				// We HAD a user session but it's invalid — access was revoked on the user's side.
				p.logger.Error("telegram_userbot: stored session is invalid, access may be revoked",
					telemetry.String("phone", p.phone),
					telemetry.Err(err))
				if err := p.persistAuthFailure(ctx, err.Error()); err != nil {
					p.logger.Warn("persist auth failure status", telemetry.Err(err))
				}
				return fmt.Errorf("auth failed: %w", err)
			} else {
				p.logger.Warn("telegram_userbot: session probe returned non-auth error, will attempt QR auth",
					telemetry.Err(err), telemetry.String("phone", p.phone))
			}
		} else {
			p.logger.Info("telegram_userbot: no user session, starting fresh QR auth",
				telemetry.String("phone", p.phone))
		}

		// 2. No valid session — QR auth loop. Telegram tokens expire every ~30 s; the
		//    client must re-export proactively because Telegram does not push a
		//    refresh notification. We loop: export → wait → if scan signal arrives → import.
		if !sessionValid {
			qr := client.QR()
		authLoop:
			for {
				token, err := qr.Export(ctx)
				if err != nil {
					return fmt.Errorf("export qr token: %w", err)
				}
				if err := p.persistQR(ctx, token); err != nil {
					p.logger.Warn("persist qr failed", telemetry.Err(err))
				}

				p.logger.Info("telegram_userbot: QR code ready, waiting for scan or manual refresh",
					telemetry.String("phone", p.phone))

				select {
				case <-ctx.Done():
					return ctx.Err()

				case <-p.qrRefreshChan:
					p.logger.Info("telegram_userbot: QR refresh requested, re-exporting",
						telemetry.String("phone", p.phone))
					continue authLoop

				case <-loggedIn:
					p.logger.Info("telegram_userbot: QR scan detected, completing login",
						telemetry.String("phone", p.phone))
					_, err := qr.Import(ctx)
					if err != nil {
						if errors.Is(err, auth.ErrPasswordAuthNeeded) || strings.Contains(err.Error(), "SESSION_PASSWORD_NEEDED") {
							if p.password2FA == "" {
								// Signal password needed instead of failing
								p.logger.Info("telegram_userbot: 2FA detected, waiting for password",
									telemetry.String("phone", p.phone))
								if err := p.persistPasswordNeeded(ctx); err != nil {
									p.logger.Warn("persist password_needed status", telemetry.Err(err))
								}
								// Wait for user to provide password via dashboard update
								select {
								case <-ctx.Done():
									return ctx.Err()
								case pwd := <-p.passwordReadyChan:
									p.password2FA = pwd
									p.logger.Info("telegram_userbot: password received, retrying auth",
										telemetry.String("phone", p.phone))
									continue authLoop
								case <-time.After(5 * time.Minute):
									p.logger.Info("telegram_userbot: 2FA password timeout",
										telemetry.String("phone", p.phone))
									return ctx.Err()
								}
							}
							p.logger.Info("telegram_userbot: 2FA needed, authenticating",
								telemetry.String("phone", p.phone))
							if _, pwdErr := client.Auth().Password(ctx, p.password2FA); pwdErr != nil {
								return fmt.Errorf("password auth: %w", pwdErr)
							}
							p.logger.Info("telegram_userbot: authenticated via 2FA",
								telemetry.String("phone", p.phone))
						} else {
							// Import returned an unexpected error — re-export and retry.
							p.logger.Warn("telegram_userbot: import error, re-exporting QR",
								telemetry.Err(err), telemetry.String("phone", p.phone))
							continue authLoop
						}
					} else {
						p.logger.Info("telegram_userbot: authenticated via QR scan",
							telemetry.String("phone", p.phone))
					}
					break authLoop
				}
			}
		}

		// 3. Persist session bytes so the next restart skips QR.
		if err := p.persistSession(ctx); err != nil {
			p.logger.Warn("telegram_userbot: persist session failed", telemetry.Err(err))
		}

		// 4. Wire the tg.UpdateDispatcher to our core.UpdateHandler.
		p.registerUpdateHandlers()

		// 5. Hold the connection until the context is cancelled.
		<-ctx.Done()
		return ctx.Err()
	})

	// If the connection failed with an auth error, persist it
	if err != nil && isAuthError(err) {
		p.logger.Error("telegram_userbot: connection failed due to auth error",
			telemetry.String("phone", p.phone),
			telemetry.Err(err))
		if err := p.persistAuthFailure(ctx, err.Error()); err != nil {
			p.logger.Warn("persist auth failure on disconnect", telemetry.Err(err))
		}
	}

	return err
}

// persistQR persists the real Telegram login token URL to the database via callback.
// Called by qrlogin.Auth every time a new token is issued (including refreshes).
func (p *Protocol) persistQR(ctx context.Context, token qrlogin.Token) error {
	p.mu.RLock()
	fn := p.optionsUpdateFunc
	accountID := p.accountID
	p.mu.RUnlock()

	url := token.URL()
	p.logger.Info("telegram_userbot: QR token issued",
		telemetry.String("phone", p.phone),
		telemetry.String("account_id", accountID),
		telemetry.String("url_prefix", url[:min(len(url), 40)]))

	if fn == nil {
		return nil
	}
	return fn(map[string]any{
		"webhook_status":       "pending",
		"qr_code_url":          url,
		"qr_token_expires_at":  token.Expires().Unix(),
	})
}

// persistPasswordNeeded signals that 2FA password is required
func (p *Protocol) persistPasswordNeeded(ctx context.Context) error {
	p.mu.RLock()
	fn := p.optionsUpdateFunc
	p.mu.RUnlock()

	if fn == nil {
		return nil
	}
	return fn(map[string]any{
		"webhook_status":  "password_needed",
		"password_prompt": "2FA is enabled. Please provide your Cloud Password.",
	})
}

// persistAuthFailure signals that authentication has failed (e.g., access revoked on phone)
func (p *Protocol) persistAuthFailure(ctx context.Context, errMsg string) error {
	p.mu.RLock()
	fn := p.optionsUpdateFunc
	p.mu.RUnlock()

	if fn == nil {
		return nil
	}
	return fn(map[string]any{
		"webhook_status": "failed",
		"webhook_last_error": errMsg,
	})
}

// persistSession base64-encodes the current session bytes and writes them to the DB.
// It also signals "webhook_status":"active" so the registry hoists that to a top-level
// PATCH field and the dashboard polling loop knows auth succeeded.
func (p *Protocol) persistSession(ctx context.Context) error {
	p.mu.RLock()
	ss := p.sessionStorage
	fn := p.optionsUpdateFunc
	p.mu.RUnlock()

	if fn == nil || ss == nil || len(ss.data) == 0 {
		return nil
	}

	encoded := base64.StdEncoding.EncodeToString(ss.data)
	// Clear force_clear_session and stale QR fields so that future restarts
	// load the saved session instead of re-entering QR auth.
	return fn(map[string]any{
		"session_data_b64":    encoded,
		"webhook_status":      "active",
		"force_clear_session": nil,
		"qr_code_url":         nil,
		"qr_token_expires_at": nil,
		"qr_code_token":       nil,
		"qr_generated_at":     nil,
	})
}

// registerUpdateHandlers wires the tg.UpdateDispatcher handlers to core.UpdateHandler.
func (p *Protocol) registerUpdateHandlers() {
	p.mu.RLock()
	handler := p.handler
	p.mu.RUnlock()

	if handler == nil {
		return
	}

	d := &UpdateDispatcher{protocol: p, handler: handler, logger: p.logger}

	p.dispatcher.OnNewMessage(func(ctx context.Context, e tg.Entities, upd *tg.UpdateNewMessage) error {
		d.handleNewMessage(ctx, upd.Message)
		return nil
	})
	p.dispatcher.OnNewChannelMessage(func(ctx context.Context, e tg.Entities, upd *tg.UpdateNewChannelMessage) error {
		d.handleNewChannelMessage(ctx, upd.Message)
		return nil
	})
	p.dispatcher.OnEditChannelMessage(func(ctx context.Context, e tg.Entities, upd *tg.UpdateEditChannelMessage) error {
		d.handleEditedMessage(ctx, upd.Message)
		return nil
	})
	p.dispatcher.OnMessageReactions(func(ctx context.Context, e tg.Entities, upd *tg.UpdateMessageReactions) error {
		d.handleReaction(ctx, upd)
		return nil
	})
	p.dispatcher.OnChatParticipant(func(ctx context.Context, e tg.Entities, upd *tg.UpdateChatParticipant) error {
		d.handleMembershipChange(ctx, upd)
		return nil
	})

	p.logger.Info("telegram_userbot: update dispatcher registered")
}

// isAuthError checks if an error indicates authentication failure
// (e.g., access revoked on phone, session invalid, unauthorized)
func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	// Check for common Telegram auth error messages
	return strings.Contains(errStr, "AUTH_KEY_UNREGISTERED") ||
		strings.Contains(errStr, "UNAUTHORIZED") ||
		strings.Contains(errStr, "SESSION_REVOKED") ||
		strings.Contains(errStr, "USER_DEACTIVATED") ||
		strings.Contains(errStr, "rpc error code 401") ||
		strings.Contains(errStr, "not authorized")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
