// Package route delivers incidents to one or more destinations.
//
// v0 ships two sinks: stdout (for development) and Slack (via
// incoming webhook URL). The interface lets future sinks (PagerDuty,
// email digest) be added without touching callers.
//
// No SLACK URL is ever embedded in the code. The URL is read from the
// INCIDENT_PILOT_SLACK_WEBHOOK env var. If empty, the Slack sink is
// silently disabled.
package route

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/anshikapundeel/incident-pilot/internal/model"
	"github.com/anshikapundeel/incident-pilot/internal/narrate"
)

// Sink delivers one incident. Implementations should not panic;
// failures are logged and ignored.
type Sink interface {
	Name() string
	Deliver(inc *model.Incident) error
}

// Router fans an incident out to all configured sinks.
type Router struct {
	Sinks []Sink
}

func (r *Router) Deliver(inc *model.Incident) {
	for _, s := range r.Sinks {
		if err := s.Deliver(inc); err != nil {
			slog.Warn("sink delivery failed",
				"sink", s.Name(), "incident", inc.ID, "err", err)
		}
	}
}

// ---- Stdout sink ----------------------------------------------------------

// StdoutSink writes the templated narration to stdout. Useful for
// development and for CI smoke tests.
type StdoutSink struct {
	Out io.Writer
}

func (s *StdoutSink) Name() string { return "stdout" }
func (s *StdoutSink) Deliver(inc *model.Incident) error {
	out := s.Out
	if out == nil {
		out = os.Stdout
	}
	body := inc.Narration
	if body == "" {
		body = narrate.Templated(inc)
	}
	fmt.Fprintln(out, "─────────────────────────────────────────────────────────")
	fmt.Fprintln(out, body)
	fmt.Fprintln(out, "─────────────────────────────────────────────────────────")
	return nil
}

// ---- Slack sink -----------------------------------------------------------

// SlackSink posts to a Slack incoming webhook. URL must be supplied at
// construction; empty URL → disabled sink.
type SlackSink struct {
	WebhookURL string
}

// NewSlackSinkFromEnv reads INCIDENT_PILOT_SLACK_WEBHOOK. Returns nil
// if unset so the caller can simply omit Slack from the router list.
func NewSlackSinkFromEnv() *SlackSink {
	if u := os.Getenv("INCIDENT_PILOT_SLACK_WEBHOOK"); u != "" {
		return &SlackSink{WebhookURL: u}
	}
	return nil
}

func (s *SlackSink) Name() string { return "slack" }

// Deliver formats the incident as Slack blocks. We use the simple
// "text" form so the message looks reasonable in any channel without
// requiring rich-formatting permissions.
func (s *SlackSink) Deliver(inc *model.Incident) error {
	if s.WebhookURL == "" {
		return nil
	}
	body := slackPayload(inc)
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(s.WebhookURL, "application/json", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("slack returned %d: %s", resp.StatusCode, string(data))
	}
	return nil
}

func slackPayload(inc *model.Incident) map[string]any {
	body := inc.Narration
	if body == "" {
		body = narrate.Templated(inc)
	}
	header := fmt.Sprintf("*Incident %s — %s — %s on %s*",
		inc.ID, inc.Severity, inc.Alert.Name(), inc.Alert.Service())
	return map[string]any{
		"text": header + "\n```\n" + body + "\n```",
	}
}
