// Package whatsapp_userbot drives a pair-linked WhatsApp account via
// whatsmeow multi-device protocol. QR pairing required on first connect.
//
// Session flow mirrors telegram_userbot:
//   - Initialize() is non-blocking: opens Postgres-backed sqlstore, resolves
//     device by JID (or creates fresh), builds client. No network calls.
//   - Listen() drives the connection and QR pairing loop, persisting QR/status
//     to the database via OptionsUpdateFunc callbacks.
package whatsapp_userbot

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/madmike/go-infra/telemetry"
	"github.com/madmike/go-messengers/core"
	_ "github.com/lib/pq"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
)

// sharedContainer is a package-level singleton Postgres-backed store shared
// across all WhatsApp userbot instances within this process.
var (
	sharedContainerOnce sync.Once
	sharedContainer     *sqlstore.Container
	sharedContainerErr  error
)

func getSharedContainer() (*sqlstore.Container, error) {
	sharedContainerOnce.Do(func() {
		dbURL := os.Getenv("WHATSAPP_USERBOT_DB_URL")
		if dbURL == "" {
			sharedContainerErr = fmt.Errorf("WHATSAPP_USERBOT_DB_URL env var not set")
			return
		}
		c, err := sqlstore.New(context.Background(), "postgres", dbURL, nil)
		if err != nil {
			sharedContainerErr = fmt.Errorf("open whatsmeow postgres store: %w", err)
			return
		}
		sharedContainer = c
	})
	return sharedContainer, sharedContainerErr
}

type Protocol struct {
	name              string
	phone             string
	logger            telemetry.Logger
	accountID         string
	optionsUpdateFunc func(map[string]any) error

	mu            sync.RWMutex
	handler       core.UpdateHandler
	client        *whatsmeow.Client
	device        *store.Device
	qrRefreshChan chan struct{}
	sentMsgIDs    map[string]struct{} // IDs of messages we sent, used to suppress echoes
	seenMsgIDs    map[string]struct{} // IDs of received messages, used to suppress multi-device duplicates
}

func NewProtocol(config core.ProviderConfig) (core.Provider, error) {
	if config.Phone == "" {
		return nil, fmt.Errorf("whatsapp_userbot: Phone (E.164) required")
	}
	logger, _ := config.Logger.(telemetry.Logger)
	if logger == nil {
		logger = &telemetry.NoOpLogger{}
	}
	return &Protocol{
		name:          config.Name,
		phone:         config.Phone,
		logger:        logger,
		qrRefreshChan: make(chan struct{}, 1),
	}, nil
}

func (p *Protocol) Name() string                  { return p.name }
func (p *Protocol) Platform() core.Platform       { return core.PlatformWhatsApp }
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

// Initialize is non-blocking. It opens (or reuses) the Postgres-backed whatsmeow
// store, resolves the device, and builds the client — but makes no network calls.
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

	container, err := getSharedContainer()
	if err != nil {
		return fmt.Errorf("whatsapp store: %w", err)
	}

	forceClear := false
	if config.Options != nil {
		if fc, ok := config.Options["force_clear_session"]; ok {
			if b, ok := fc.(bool); ok {
				forceClear = b
			}
		}
	}

	// Resolve existing JID from options
	var existingJID types.JID
	hasJID := false
	if config.Options != nil {
		if jidStr, ok := config.Options["whatsapp_jid"].(string); ok && jidStr != "" {
			parsed, parseErr := types.ParseJID(jidStr)
			if parseErr == nil {
				existingJID = parsed
				hasJID = true
			}
		}
	}

	var device *store.Device

	if forceClear && hasJID {
		// Delete the old device to force fresh pairing
		existing, err := container.GetDevice(ctx, existingJID)
		if err == nil && existing != nil {
			if err := container.DeleteDevice(ctx, existing); err != nil {
				p.logger.Warn("whatsapp_userbot: could not delete old device on force_clear",
					telemetry.Err(err))
			} else {
				p.logger.Info("whatsapp_userbot: force_clear_session, deleted old device",
					telemetry.String("jid", existingJID.String()))
			}
		}
		device = container.NewDevice()
	} else if hasJID {
		existing, err := container.GetDevice(ctx, existingJID)
		if err != nil || existing == nil {
			p.logger.Warn("whatsapp_userbot: stored JID not found in store, creating new device",
				telemetry.String("jid", existingJID.String()))
			device = container.NewDevice()
		} else {
			device = existing
		}
	} else {
		device = container.NewDevice()
	}

	p.device = device
	c := whatsmeow.NewClient(device, nil)
	c.AddEventHandler(p.handleEvent)
	p.client = c

	p.logger.Info("whatsapp_userbot: initialized",
		telemetry.String("phone", p.phone),
		telemetry.String("account_id", p.accountID),
		telemetry.Bool("has_jid", hasJID),
		telemetry.Bool("force_clear", forceClear))
	return nil
}

func (p *Protocol) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil {
		p.client.Disconnect()
		p.client = nil
	}
	return nil
}

// Disconnect logs out the WhatsApp device and clears the client.
func (p *Protocol) Disconnect(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.client == nil {
		return fmt.Errorf("whatsapp_userbot: not initialized")
	}

	if p.client.IsLoggedIn() {
		if err := p.client.Logout(ctx); err != nil {
			p.logger.Warn("whatsapp_userbot: logout error", telemetry.Err(err))
		}
	}
	p.client.Disconnect()
	p.client = nil
	p.logger.Info("whatsapp_userbot: disconnected", telemetry.String("phone", p.phone))
	return nil
}

// RefreshQR signals the auth loop to re-export a fresh QR code.
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
		return fmt.Errorf("whatsapp_userbot: not initialized")
	}
	if !c.IsConnected() {
		return fmt.Errorf("not connected")
	}
	if !c.IsLoggedIn() {
		return fmt.Errorf("not logged in")
	}
	return nil
}

func (p *Protocol) Me(ctx context.Context) (*core.Account, error) {
	p.mu.RLock()
	c := p.client
	p.mu.RUnlock()

	if c == nil {
		return nil, fmt.Errorf("whatsapp_userbot: not initialized")
	}
	if !c.IsLoggedIn() {
		return nil, fmt.Errorf("not logged in")
	}
	jid := c.Store.ID
	if jid == nil {
		return nil, fmt.Errorf("no jid")
	}
	return &core.Account{
		ID:          jid.User,
		Platform:    core.PlatformWhatsApp,
		AccountType: core.AccountTypeUserbot,
		Phone:       p.phone,
		IsBot:       false,
	}, nil
}

// Listen drives the WhatsApp connection and QR pairing loop.
// It blocks until ctx is cancelled, mirroring telegram_userbot.run.
func (p *Protocol) Listen(ctx context.Context, handler core.UpdateHandler) error {
	if handler == nil {
		return fmt.Errorf("whatsapp_userbot: nil handler")
	}
	p.mu.Lock()
	p.handler = handler
	p.mu.Unlock()

	return p.run(ctx)
}

func (p *Protocol) Poll(ctx context.Context, handler core.UpdateHandler) error {
	return p.Listen(ctx, handler)
}

func (p *Protocol) HTTPHandler() core.HTTPHandler { return nil }

// run is the main connection + auth loop.
func (p *Protocol) run(ctx context.Context) error {
	p.mu.RLock()
	client := p.client
	device := p.device
	p.mu.RUnlock()

	if client == nil {
		return fmt.Errorf("whatsapp_userbot: Initialize not called")
	}

	// whatsmeow requires GetQRChannel to be called BEFORE Connect when there is no
	// saved device JID. If a JID exists the session can be restored by connecting directly.
	needsQR := device == nil || device.ID == nil

	if needsQR {
		// GetQRChannel must precede Connect — start the pairing flow in a goroutine
		// so Connect can proceed and the QR channel receives events.
		qrChan, err := client.GetQRChannel(ctx)
		if err != nil {
			if err2 := p.persistAuthFailure(ctx, err.Error()); err2 != nil {
				p.logger.Warn("persist auth failure", telemetry.Err(err2))
			}
			return fmt.Errorf("get QR channel: %w", err)
		}

		p.logger.Info("whatsapp_userbot: connecting for QR pairing", telemetry.String("phone", p.phone))
		if err := client.Connect(); err != nil {
			if err2 := p.persistAuthFailure(ctx, err.Error()); err2 != nil {
				p.logger.Warn("persist auth failure", telemetry.Err(err2))
			}
			return fmt.Errorf("connect: %w", err)
		}

		if err := p.drainQRChan(ctx, client, qrChan); err != nil {
			return err
		}
	} else {
		p.logger.Info("whatsapp_userbot: connecting with saved session", telemetry.String("phone", p.phone))
		if err := client.Connect(); err != nil {
			if err2 := p.persistAuthFailure(ctx, err.Error()); err2 != nil {
				p.logger.Warn("persist auth failure", telemetry.Err(err2))
			}
			return fmt.Errorf("connect: %w", err)
		}
		p.logger.Info("whatsapp_userbot: session restored", telemetry.String("phone", p.phone))
	}

	// Hold the connection until ctx is cancelled
	<-ctx.Done()
	return ctx.Err()
}

// drainQRChan reads from a QR channel (obtained before Connect) until pairing
// succeeds, times out, or an error occurs. On timeout whatsmeow closes the
// channel; the caller must reconnect and call GetQRChannel again to refresh.
func (p *Protocol) drainQRChan(ctx context.Context, client *whatsmeow.Client, qrChan <-chan whatsmeow.QRChannelItem) error {
	p.logger.Info("whatsapp_userbot: waiting for QR pairing", telemetry.String("phone", p.phone))

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case qrItem, ok := <-qrChan:
			if !ok {
				// Channel closed — pairing concluded (success already handled or terminal error)
				return nil
			}
			switch qrItem.Event {
			case "success":
				p.logger.Info("whatsapp_userbot: QR pairing successful", telemetry.String("phone", p.phone))
				jid := ""
				if client.Store.ID != nil {
					jid = client.Store.ID.String()
				}
				if err := p.persistActive(ctx, jid); err != nil {
					p.logger.Warn("whatsapp_userbot: persist active failed", telemetry.Err(err))
				}
				return nil

			case "timeout":
				p.logger.Info("whatsapp_userbot: QR timed out (server closed session)",
					telemetry.String("phone", p.phone))
				if err2 := p.persistAuthFailure(ctx, "QR code timed out — reactivate to retry"); err2 != nil {
					p.logger.Warn("persist auth failure", telemetry.Err(err2))
				}
				return nil

			case "code":
				p.logger.Info("whatsapp_userbot: QR code ready", telemetry.String("phone", p.phone))
				if err := p.persistQR(ctx, qrItem.Code); err != nil {
					p.logger.Warn("persist QR failed", telemetry.Err(err))
				}

			default:
				if qrItem.Error != nil {
					if err2 := p.persistAuthFailure(ctx, qrItem.Error.Error()); err2 != nil {
						p.logger.Warn("persist auth failure", telemetry.Err(err2))
					}
					return fmt.Errorf("qr pairing error: %w", qrItem.Error)
				}
				p.logger.Info("whatsapp_userbot: QR event",
					telemetry.String("event", qrItem.Event),
					telemetry.String("phone", p.phone))
			}
		}
	}
}

// --- persist helpers ---

func (p *Protocol) persistQR(ctx context.Context, code string) error {
	p.mu.RLock()
	fn := p.optionsUpdateFunc
	p.mu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn(map[string]any{
		"webhook_status": "pending",
		"qr_code_url":    code,
	})
}

func (p *Protocol) persistActive(ctx context.Context, jid string) error {
	p.mu.RLock()
	fn := p.optionsUpdateFunc
	p.mu.RUnlock()
	if fn == nil {
		return nil
	}
	update := map[string]any{
		"webhook_status":      "active",
		"qr_code_url":         nil,
		"force_clear_session": nil,
	}
	if jid != "" {
		update["whatsapp_jid"] = jid
	}
	return fn(update)
}

func (p *Protocol) persistAuthFailure(ctx context.Context, errMsg string) error {
	p.mu.RLock()
	fn := p.optionsUpdateFunc
	p.mu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn(map[string]any{
		"webhook_status":     "failed",
		"webhook_last_error": errMsg,
	})
}

// --- GroupManager and FileManager ---
// (Implemented in groups.go and files.go)
