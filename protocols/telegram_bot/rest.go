package telegram_bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
)

// apiCall invokes a Telegram Bot API method. If `body` is a
// *multipartBody, the request is sent as multipart/form-data (for file
// uploads); otherwise JSON is used.
func (p *Protocol) apiCall(ctx context.Context, method string, body any, out any) error {
	url := fmt.Sprintf("%s/bot%s/%s", p.baseURL, p.token, method)

	var (
		req *http.Request
		err error
	)

	if mp, ok := body.(*multipartBody); ok {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, mp.buf)
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", mp.contentType)
	} else if body == nil {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
	} else {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("telegram_bot: encode %s: %w", method, err)
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("telegram_bot: %s: %w", method, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("telegram_bot: read %s: %w", method, err)
	}

	var envelope struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		ErrorCode   int             `json:"error_code"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("telegram_bot: decode %s: %w (body=%s)", method, err, truncate(string(raw), 256))
	}
	if !envelope.OK {
		return fmt.Errorf("telegram_bot: %s failed (%d): %s", method, envelope.ErrorCode, envelope.Description)
	}
	if out == nil || len(envelope.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(envelope.Result, out); err != nil {
		return fmt.Errorf("telegram_bot: decode result %s: %w", method, err)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// multipartBody is passed to apiCall to request multipart/form-data encoding.
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
