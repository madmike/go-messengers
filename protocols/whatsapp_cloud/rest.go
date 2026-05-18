package whatsapp_cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
)

// graphCall POSTs JSON or multipart against /{version}/{path} on the Graph API.
// If body is *multipartBody, a multipart/form-data request is sent; otherwise
// body is JSON-marshalled. `out` is populated from the response body on 2xx.
func (p *Protocol) graphCall(ctx context.Context, path string, body any, out any) error {
	endpoint := fmt.Sprintf("%s/%s/%s", p.baseURL, p.version, path)

	var (
		req *http.Request
		err error
	)
	if mp, ok := body.(*multipartBody); ok {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, endpoint, mp.buf)
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", mp.contentType)
	} else if body == nil {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
	} else {
		data, merr := json.Marshal(body)
		if merr != nil {
			return fmt.Errorf("whatsapp_cloud: encode %s: %w", path, merr)
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+p.token)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("whatsapp_cloud: %s: %w", path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("whatsapp_cloud: read %s: %w", path, err)
	}
	if resp.StatusCode >= 400 {
		return decodeGraphError(path, resp.StatusCode, raw)
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("whatsapp_cloud: decode %s: %w (body=%s)", path, err, truncate(string(raw), 256))
	}
	return nil
}

// graphGet issues GET /{version}/{path}?query.
func (p *Protocol) graphGet(ctx context.Context, path string, query url.Values, out any) error {
	endpoint := fmt.Sprintf("%s/%s/%s", p.baseURL, p.version, path)
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("whatsapp_cloud: GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return decodeGraphError(path, resp.StatusCode, raw)
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("whatsapp_cloud: decode %s: %w", path, err)
	}
	return nil
}

func decodeGraphError(path string, status int, raw []byte) error {
	var envelope struct {
		Error struct {
			Message   string `json:"message"`
			Type      string `json:"type"`
			Code      int    `json:"code"`
			Subcode   int    `json:"error_subcode"`
			FBTraceID string `json:"fbtrace_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Error.Message != "" {
		return fmt.Errorf("whatsapp_cloud: %s HTTP %d (%s/%d): %s",
			path, status, envelope.Error.Type, envelope.Error.Code, envelope.Error.Message)
	}
	return fmt.Errorf("whatsapp_cloud: %s HTTP %d: %s", path, status, truncate(string(raw), 256))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// multipartBody mirrors the Telegram bot's envelope — re-used by media upload.
type multipartBody struct {
	buf         *bytes.Buffer
	contentType string
}

func newMultipart(fields map[string]string, file *multipartFile) (*multipartBody, error) {
	buf := &bytes.Buffer{}
	w := multipart.NewWriter(buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return nil, err
		}
	}
	if file != nil {
		fw, err := w.CreateFormFile(file.field, file.filename)
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(file.data); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return &multipartBody{buf: buf, contentType: w.FormDataContentType()}, nil
}

type multipartFile struct {
	field    string
	filename string
	data     []byte
}
