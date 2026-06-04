# go-messengers

Messenger integration library for the Aulinq platform. Provides a unified
interface for Telegram and WhatsApp (official APIs + userbots), with room to
grow to Viber, VK, Facebook Messenger, and Instagram.

Mirrors the design of `go-ai-providers` and `go-voip`:
- protocol-agnostic interfaces in `core/`
- one Go package per protocol under `protocols/`
- named presets + `CreateFromDB` in `factory/`

## Architecture

```
core.Provider            ← base: Name / Platform / AccountType / Capabilities
  ├── core.Messenger      ← Send / Edit / Delete (outbound)
  ├── core.Receiver       ← Listen / Poll / HTTPHandler (inbound)
  ├── core.GroupManager   ← GetChat / ListMembers / Add / Remove / ListSubChats
  └── core.FileManager    ← UploadFile / DownloadFile
```

Services type-assert against the capability interfaces they need, rather than
hard-coupling to a specific platform. A protocol advertises what it supports
via `Capabilities()` — call `SupportsCapability(…)` before casting.

### Group hierarchy

`core.Chat.ParentID` expresses sub-chats:

- Telegram forum **topic** → `ParentID = supergroup id`
- WhatsApp **group in a community** → `ParentID = community id`
- Future platforms with channel sub-channels follow the same pattern.

`GroupManager.ListSubChats(parent)` returns the child chats. Providers that
can't enumerate sub-chats (Telegram Bot API) return an empty slice; MTProto /
userbot implementations will fill this in.

## Protocols

| Preset              | Platform   | Account type   | Status        |
|---------------------|------------|----------------|---------------|
| `telegram_bot`      | Telegram   | Bot            | **Complete**  |
| `telegram_userbot`  | Telegram   | Userbot        | Skeleton (needs `gotd/td`) |
| `whatsapp_cloud`    | WhatsApp   | Business API   | **Complete**  |
| `whatsapp_userbot`  | WhatsApp   | Userbot        | Skeleton (needs `whatsmeow`) |

### Telegram Bot

- Webhook (push) and long-polling (`getUpdates`) supported.
- Media upload via `UploadFile` requires `Options["storage_chat_id"]` — the
  Bot API has no standalone upload endpoint; we send to a throwaway chat.
- `ListMembers` returns admins only (Bot API restriction).
- `AddMembers` is not supported (bots can't invite).

### WhatsApp Cloud API

- Graph API v20 by default (override with `Options["graph_version"]`).
- Webhook verification handshake supported via `hub.verify_token` → set
  `Options["verify_token"]`.
- Signed webhooks via `X-Hub-Signature-256` — set `APISecret` to the app secret.
- `Edit` / `Delete` return errors; WA Cloud API does not support them.

## Usage

```go
import (
    "github.com/madmike/go-messengers/core"
    "github.com/madmike/go-messengers/factory"
)

provider, err := factory.CreateFromDB(factory.DBProviderConfig{
    PresetName:       "telegram_bot",
    DisplayName:      "ACME support bot",
    APIKey:           botToken,
    PublicWebhookURL: "https://hooks.acme.com/tg",
    WebhookSecret:    tgSecret,
})

if sender, ok := provider.(core.Messenger); ok {
    sender.Send(ctx, core.OutgoingMessage{
        ChatID: chatID,
        Text:   "Hello 👋",
    })
}
```

## DB schema

Messenger account credentials live in `tenant_providers` (kind = `messenger`).
Per-account metadata (platform, account_type, phone number for userbots,
external account id for cloud API) lives in `messenger_accounts`. Chat
metadata (for hierarchy + subscriber tracking) will live in a `chats` table
with a self-referencing `parent_chat_id`.

See `db/migrations/20260130000001_tenants_and_assistants.sql`.
