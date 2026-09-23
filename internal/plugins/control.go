package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Control performs an authenticated request against a ready plugin. The
// control address and per-process secret intentionally never leave this
// package or the public API.
func (m *Manager) Control(ctx context.Context, id, method, path string, request, response any) error {
	p, err := m.plugin(id)
	if err != nil {
		return err
	}
	p.mu.Lock()
	address, secret, ready := p.address, p.secret, p.ready
	p.mu.Unlock()
	if !ready || address == "" || secret == "" {
		return fmt.Errorf("plugin %q is not ready", id)
	}
	if err := control(address, secret, ctx, method, path, request, response); err != nil {
		return fmt.Errorf("plugin %q control: %w", id, err)
	}
	return nil
}

func control(address, secret string, ctx context.Context, method, path string, request, response any) error {
	var body *bytes.Reader
	if request == nil {
		body = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(request)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+address+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	if request != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s", resp.Status)
	}
	if response != nil {
		return json.NewDecoder(resp.Body).Decode(response)
	}
	return nil
}
