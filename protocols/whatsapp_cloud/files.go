package whatsapp_cloud

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/madmike/go-messengers/core"
)

// UploadFile uploads media via POST /{phone_number_id}/media. Returns a
// core.FileRef whose ID is the WA media id (valid ~30 days).
func (p *Protocol) UploadFile(ctx context.Context, file core.FileUpload) (*core.FileRef, error) {
	if file.MimeType == "" {
		return nil, fmt.Errorf("whatsapp_cloud: UploadFile requires MimeType")
	}
	fields := map[string]string{
		"messaging_product": "whatsapp",
		"type":              file.MimeType,
	}
	mp, err := newMultipart(fields, &multipartFile{
		field:    "file",
		filename: firstNonEmpty(file.FileName, "upload.bin"),
		data:     file.Data,
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		ID string `json:"id"`
	}
	path := fmt.Sprintf("%s/media", p.phoneNumberID)
	if err := p.graphCall(ctx, path, mp, &resp); err != nil {
		return nil, err
	}
	return &core.FileRef{
		Platform: core.PlatformWhatsApp,
		ID:       resp.ID,
		MimeType: file.MimeType,
		Size:     int64(len(file.Data)),
	}, nil
}

// DownloadFile resolves a media_id to bytes by hitting GET /{media_id} for the
// URL, then fetching that URL with the access token attached.
func (p *Protocol) DownloadFile(ctx context.Context, mediaID string) ([]byte, *core.FileRef, error) {
	var meta struct {
		URL      string `json:"url"`
		MimeType string `json:"mime_type"`
		FileSize int64  `json:"file_size"`
		ID       string `json:"id"`
	}
	if err := p.graphGet(ctx, mediaID, nil, &meta); err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, meta.URL, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("whatsapp_cloud: download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, nil, fmt.Errorf("whatsapp_cloud: download HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	return data, &core.FileRef{
		Platform: core.PlatformWhatsApp,
		ID:       meta.ID,
		URL:      meta.URL,
		Size:     meta.FileSize,
		MimeType: meta.MimeType,
	}, nil
}
