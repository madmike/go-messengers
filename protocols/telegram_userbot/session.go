package telegram_userbot

import (
	"context"
	"encoding/base64"
)

// MemorySessionStorage implements session.Storage using an in-memory map.
// In production, this would be backed by a database, but for now we use
// the options JSONB field in messenger_accounts to persist sessions.
type MemorySessionStorage struct {
	data []byte
}

func (s *MemorySessionStorage) LoadSession(ctx context.Context) ([]byte, error) {
	return s.data, nil
}

func (s *MemorySessionStorage) StoreSession(ctx context.Context, data []byte) error {
	s.data = data
	return nil
}

// SessionCodec handles encoding/decoding session data to/from base64 for storage.
type SessionCodec struct {
	data []byte
}

func (sc *SessionCodec) Encode() string {
	return base64.StdEncoding.EncodeToString(sc.data)
}

func (sc *SessionCodec) Decode(s string) error {
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	sc.data = data
	return nil
}

func (sc *SessionCodec) Data() []byte {
	return sc.data
}
