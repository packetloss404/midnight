package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/webhook"
)

func TestRegisterWebhookHandlersWiresPanelPaths(t *testing.T) {
	resolver := webhook.NewMemoryEndpointResolver()
	resolver.Set("ep1", webhook.EndpointConfig{Provider: webhook.ProviderMidnightIncoming, Keys: []webhook.Key{{KID: "k", Material: []byte("secret")}}})
	receiver := webhook.NewReceiver(webhook.Options{Resolver: resolver, Clock: func() time.Time { return time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC) }})

	mux := http.NewServeMux()
	RegisterWebhookHandlers(mux, WebhookHandlerOptions{Receiver: receiver})

	// Ensure the panel-locked path resolves to a handler. We hit it
	// without a body and expect the receiver's signature-malformed
	// error path, which proves the handler is registered.
	req := httptest.NewRequest(http.MethodPost, WebhookRoutePrefix+"midnight-incoming/ep1", bytes.NewReader([]byte("{}")))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound {
		t.Fatalf("handler not registered; rec=%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "WEBHOOK_SIGNATURE_INVALID") {
		t.Fatalf("expected signature error envelope; got %s", rec.Body.String())
	}
}

func TestRegisterWebhookHandlersHandlesNilReceiver(t *testing.T) {
	mux := http.NewServeMux()
	RegisterWebhookHandlers(mux, WebhookHandlerOptions{})
	// Should not panic, should not register routes.
	req := httptest.NewRequest(http.MethodPost, WebhookRoutePrefix+"midnight-incoming/ep1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}
