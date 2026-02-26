package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/pkg/random"
	"github.com/inconshreveable/log15"
)

const (
	webhookBufferSize  = 256
	webhookTimeout     = 5 * time.Second
	webhookMaxRetries  = 2
	webhookRetryDelay  = 1 * time.Second
)

// WebhookDispatcher dispatches webhook events to configured endpoints.
// It runs in its own goroutine and uses a buffered channel to avoid blocking event producers.
type WebhookDispatcher struct {
	hostID string
	state  *State
	events chan *host.WebhookEvent
	done   chan struct{}
	log    log15.Logger
	client *http.Client
}

// NewWebhookDispatcher creates a new dispatcher. Call Run() to start processing events.
func NewWebhookDispatcher(hostID string, state *State, log log15.Logger) *WebhookDispatcher {
	return &WebhookDispatcher{
		hostID: hostID,
		state:  state,
		events: make(chan *host.WebhookEvent, webhookBufferSize),
		done:   make(chan struct{}),
		log:    log.New("component", "webhook-dispatcher"),
		client: &http.Client{Timeout: webhookTimeout},
	}
}

// Run starts the dispatcher loop. Should be called in a goroutine.
func (d *WebhookDispatcher) Run() {
	d.log.Info("webhook dispatcher started")
	for {
		select {
		case event, ok := <-d.events:
			if !ok {
				d.log.Info("webhook dispatcher stopped")
				return
			}
			d.dispatch(event)
		case <-d.done:
			d.log.Info("webhook dispatcher shutting down")
			return
		}
	}
}

// Shutdown stops the dispatcher.
func (d *WebhookDispatcher) Shutdown() {
	close(d.done)
}

// Send enqueues a webhook event for delivery. It is non-blocking; if the buffer is full the event is dropped.
func (d *WebhookDispatcher) Send(code, description, severity string, jobID string, job *host.ActiveJob, metadata map[string]string) {
	event := &host.WebhookEvent{
		EventID:     random.UUID(),
		Timestamp:   time.Now().UTC(),
		HostID:      d.hostID,
		Code:        code,
		Description: description,
		Severity:    severity,
		JobID:       jobID,
		Job:         job,
		Metadata:    metadata,
	}
	select {
	case d.events <- event:
	default:
		d.log.Warn("webhook event buffer full, dropping event", "code", code, "event_id", event.EventID)
	}
}

// dispatch sends an event to all configured webhooks.
func (d *WebhookDispatcher) dispatch(event *host.WebhookEvent) {
	webhooks := d.state.ListWebhooks()
	if len(webhooks) == 0 {
		return
	}

	payload, err := json.Marshal(event)
	if err != nil {
		d.log.Error("error marshaling webhook event", "err", err, "code", event.Code)
		return
	}

	for _, wh := range webhooks {
		go d.deliver(wh.URL, payload, event.EventID)
	}
}

// deliver sends the payload to a single URL with retry logic.
func (d *WebhookDispatcher) deliver(url string, payload []byte, eventID string) {
	var lastErr error
	for attempt := 0; attempt <= webhookMaxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(webhookRetryDelay)
		}
		resp, err := d.client.Post(url, "application/json", bytes.NewReader(payload))
		if err != nil {
			lastErr = err
			d.log.Warn("webhook delivery failed", "url", url, "event_id", eventID, "attempt", attempt+1, "err", err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return // success
		}
		d.log.Warn("webhook delivery non-2xx response", "url", url, "event_id", eventID, "attempt", attempt+1, "status", resp.StatusCode)
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return // client error, don't retry
		}
		lastErr = nil // server error, will retry
	}
	if lastErr != nil {
		d.log.Error("webhook delivery exhausted retries", "url", url, "event_id", eventID, "err", lastErr)
	} else {
		d.log.Error("webhook delivery exhausted retries", "url", url, "event_id", eventID)
	}
}

