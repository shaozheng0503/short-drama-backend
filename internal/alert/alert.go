package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"ai-drama-platform/internal/config"
)

type Client struct {
	enabled bool
	url     string
	timeout time.Duration
	http    *http.Client
}

type Event struct {
	Level   string                 `json:"level"`
	Type    string                 `json:"type"`
	Message string                 `json:"message"`
	Fields  map[string]interface{} `json:"fields,omitempty"`
	At      time.Time              `json:"at"`
}

func New(cfg config.Config) *Client {
	return &Client{
		enabled: cfg.AlertEnabled,
		url:     cfg.AlertWebhookURL,
		timeout: cfg.AlertTimeout,
		http:    &http.Client{Timeout: cfg.AlertTimeout},
	}
}

func (c *Client) Enabled() bool {
	return c != nil && c.enabled && c.url != ""
}

func (c *Client) SendAsync(event Event) {
	if !c.Enabled() {
		return
	}
	go func() {
		if err := c.Send(context.Background(), event); err != nil {
			log.Printf("[alert] send failed type=%s err=%v", event.Type, err)
		}
	}()
}

func (c *Client) Send(ctx context.Context, event Event) error {
	if !c.Enabled() {
		return nil
	}
	if event.At.IsZero() {
		event.At = time.Now()
	}
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("[alert] webhook returned status=%d", resp.StatusCode)
	}
	return nil
}
