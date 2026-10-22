package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Runtime interface {
	Reload(context.Context, string) error
	Ready(context.Context) error
}

type HTTPRuntime struct {
	BaseURL string
	Secret  string
	Client  *http.Client
}

func (r *HTTPRuntime) configured() bool { return strings.TrimSpace(r.BaseURL) != "" }
func (r *HTTPRuntime) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return &http.Client{Timeout: 8 * time.Second}
}
func (r *HTTPRuntime) Reload(ctx context.Context, path string) error {
	if !r.configured() {
		return errors.New("Mihomo controller is not configured; deployment remains unverified")
	}
	payload, _ := json.Marshal(map[string]any{"path": path, "payload": ""})
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, strings.TrimRight(r.BaseURL, "/")+"/configs?force=true", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+r.Secret)
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return fmt.Errorf("reload result is uncertain: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Mihomo rejected configuration with HTTP %d", resp.StatusCode)
	}
	return nil
}
func (r *HTTPRuntime) Ready(ctx context.Context) error {
	if !r.configured() {
		return errors.New("Mihomo controller is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(r.BaseURL, "/")+"/version", nil)
	if err != nil {
		return err
	}
	if r.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+r.Secret)
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Mihomo controller health returned HTTP %d", resp.StatusCode)
	}
	return nil
}
