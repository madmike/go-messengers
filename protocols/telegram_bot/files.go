package telegram_bot

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/madmike/go-messengers/core"
)

// UploadFile uploads media to Telegram by issuing a throwaway sendDocument
// and returning the resulting file_id. Telegram doesn't expose a standalone
// "upload and get id" endpoint; this pattern is the canonical workaround —
// typically you'd send to a dedicated storage chat owned by the bot.
//
// If Options["storage_chat_id"] is set, that chat is used; otherwise the
// caller must supply it via FileUpload (extension not yet wired).
func (p *Protocol) UploadFile(ctx context.Context, file core.FileUpload) (*core.FileRef, error) {
	storageChat, _ := p.lookupOption("storage_chat_id")
	if storageChat == "" {
		return nil, fmt.Errorf("telegram_bot: UploadFile requires Options[\"storage_chat_id\"]")
	}
	mp, err := newMultipart(map[string]string{
		"chat_id":              storageChat,
		"disable_notification": "true",
	}, &multipartFile{
		field:    "document",
		filename: firstNonEmpty(file.FileName, "upload.bin"),
		data:     file.Data,
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Document telegramDocument `json:"document"`
	}
	if err := p.apiCall(ctx, "sendDocument", mp, &resp); err != nil {
		return nil, err
	}
	return &core.FileRef{
		Platform: core.PlatformTelegram,
		ID:       resp.Document.FileID,
		Size:     resp.Document.FileSize,
		MimeType: resp.Document.MimeType,
	}, nil
}

// DownloadFile resolves a Telegram file_id to bytes via getFile + CDN fetch.
func (p *Protocol) DownloadFile(ctx context.Context, fileID string) ([]byte, *core.FileRef, error) {
	body := map[string]any{"file_id": fileID}
	var meta struct {
		FileID   string `json:"file_id"`
		FilePath string `json:"file_path"`
		FileSize int64  `json:"file_size"`
	}
	if err := p.apiCall(ctx, "getFile", body, &meta); err != nil {
		return nil, nil, err
	}
	url := fmt.Sprintf("%s/file/bot%s/%s", p.baseURL, p.token, meta.FilePath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("telegram_bot: download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, nil, fmt.Errorf("telegram_bot: download HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	return data, &core.FileRef{
		Platform: core.PlatformTelegram,
		ID:       meta.FileID,
		Size:     meta.FileSize,
		URL:      url,
	}, nil
}

func (p *Protocol) lookupOption(key string) (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.me == nil {
		return "", false
	}
	// Options captured at Initialize-time aren't stored on Protocol; keep a
	// hook here so future callers can wire a per-provider storage_chat_id.
	return "", false
}
