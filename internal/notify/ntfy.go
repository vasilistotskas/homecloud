// Package notify sends push notifications through ntfy (https://docs.ntfy.sh/publish).
package notify

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/vasilistotskas/homecloud/internal/config"
)

// Priority follows ntfy's 1 (min) .. 5 (max) scale.
type Priority int

// ntfy priorities.
const (
	PriorityLow     Priority = 2
	PriorityDefault Priority = 3
	PriorityHigh    Priority = 4
	PriorityUrgent  Priority = 5
)

// Msg is a notification. Messages must not contain file names or other
// library contents: ntfy.sh topics are public to anyone who knows the name.
type Msg struct {
	Title    string
	Body     string
	Priority Priority
	Tags     []string
}

// Ntfy publishes to one topic.
type Ntfy struct {
	server, topic string
	client        *http.Client
}

const requestTimeout = 30 * time.Second

// New returns a notifier for cfg.
func New(cfg config.Notify) *Ntfy {
	return &Ntfy{server: cfg.Server, topic: cfg.Topic, client: &http.Client{Timeout: requestTimeout}}
}

// publishRequest is ntfy's JSON publish body (POST to the server root).
type publishRequest struct {
	Topic    string   `json:"topic"`
	Title    string   `json:"title,omitempty"`
	Message  string   `json:"message"`
	Priority Priority `json:"priority,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}

// Send publishes m and returns an error unless ntfy answers 200 OK.
func (n *Ntfy) Send(ctx context.Context, m Msg) error {
	body, err := json.Marshal(publishRequest{Topic: n.topic, Title: m.Title, Message: m.Body, Priority: m.Priority, Tags: m.Tags})
	if err != nil {
		return fmt.Errorf("encode ntfy message: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.server, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build ntfy request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("ntfy publish: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // response fully handled below
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512)) //nolint:errcheck // best-effort error detail
		return fmt.Errorf("ntfy publish: HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(detail))
	}
	return nil
}

// TestMsg returns the message sent to confirm that notifications arrive.
func TestMsg() Msg {
	return Msg{
		Title:    "homecloud test",
		Body:     "Notifications from homecloud reach this device.",
		Priority: PriorityDefault,
		Tags:     []string{"white_check_mark"},
	}
}
