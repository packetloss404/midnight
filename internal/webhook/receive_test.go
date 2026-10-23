package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/cloudevents"
)

type captureAudit struct {
	mu sync.Mutex
	in []DeliveryAudit
}

func (c *captureAudit) EmitDelivery(_ context.Context, ev DeliveryAudit) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.in = append(c.in, ev)
}

func (c *captureAudit) snapshot() []DeliveryAudit {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]DeliveryAudit, len(c.in))
	copy(out, c.in)
	return out
}

type captureDispatcher struct {
	mu sync.Mutex
	in []ReceivedDelivery
}

func (c *captureDispatcher) Dispatch(_ context.Context, d ReceivedDelivery) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.in = append(c.in, d)
	return nil
}

func (c *captureDispatcher) snapshot() []ReceivedDelivery {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ReceivedDelivery, len(c.in))
	copy(out, c.in)
	return out
}

type captureInboxAcceptance struct {
	DurableInbox
	requests []InboxAcceptance
	result   AcceptResult
	err      error
}

func (c *captureInboxAcceptance) Accept(
	_ context.Context,
	request InboxAcceptance,
) (AcceptResult, error) {
	request.Payload = append([]byte(nil), request.Payload...)
	c.requests = append(c.requests, request)
	return c.result, c.err
}

func validIncoming(t *testing.T) (cloudevents.Event, []byte) {
	t.Helper()
	ev := cloudevents.NewEvent()
	ev.ID = "evt_test"
	ev.Source = "urn:midnight:test:project:wsp_x"
	ev.Subject = "deployment/dep_1"
	ev.Type = cloudevents.TypeDeploymentQueued
	ev.TraceParent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	ev.MidnightRequestID = "req_x"
	ev.MidnightTenantID = "wsp_x"
	ev.Time = time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	body, err := cloudevents.Encode(ev)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return ev, body
}

func TestReceiverRejectsNonPOSTBeforeEndpointOrSignatureWork(t *testing.T) {
	receiver := NewReceiver(Options{})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1alpha/webhooks/inbound/midnight-incoming/unknown", nil)
	receiver.HandleInbound(ProviderMidnightIncoming).ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("Allow=%q, want POST", response.Header().Get("Allow"))
	}
}

func TestReceiverAcceptsValidDeliveryEmitsAudit(t *testing.T) {
	ts := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	resolver := NewMemoryEndpointResolver()
	resolver.Set("ep1", EndpointConfig{
		Provider: ProviderMidnightIncoming,
		Keys:     []Key{{KID: "whk_a", Material: []byte("secret-shared-with-ci")}},
	})
	audit := &captureAudit{}
	dispatcher := &captureDispatcher{}
	receiver := NewReceiver(Options{
		Resolver:   resolver,
		Dedupe:     NewMemoryDedupeStore(),
		Clock:      func() time.Time { return ts },
		Audit:      audit,
		Dispatcher: dispatcher,
	})

	_, body := validIncoming(t)
	header, err := Signer{}.SignBody([]Key{{KID: "whk_a", Material: []byte("secret-shared-with-ci")}}, body, ts)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/webhooks/inbound/midnight-incoming/ep1", bytes.NewReader(body))
	req.Header.Set(SignatureHeader, header)
	req.Header.Set(DeliveryHeader, "whd_001")
	req.Header.Set("Content-Type", cloudevents.ContentType)
	rec := httptest.NewRecorder()
	receiver.HandleInbound(ProviderMidnightIncoming).ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := dispatcher.snapshot(); len(got) != 1 {
		t.Fatalf("dispatcher got %d deliveries", len(got))
	}
	deliveries := audit.snapshot()
	if len(deliveries) != 1 || deliveries[0].Outcome != "accepted" {
		t.Fatalf("audit emit = %+v", deliveries)
	}
	if deliveries[0].DeliveryID != "whd_001" {
		t.Fatalf("audit delivery id = %s", deliveries[0].DeliveryID)
	}
}

func TestReceiverRejectsReplayedDelivery(t *testing.T) {
	ts := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	resolver := NewMemoryEndpointResolver()
	resolver.Set("ep1", EndpointConfig{Provider: ProviderMidnightIncoming, Keys: []Key{{KID: "k", Material: []byte("secret")}}})
	audit := &captureAudit{}
	dispatcher := &captureDispatcher{}
	dedupe := NewMemoryDedupeStore()
	receiver := NewReceiver(Options{Resolver: resolver, Dedupe: dedupe, Clock: func() time.Time { return ts }, Audit: audit, Dispatcher: dispatcher})

	_, body := validIncoming(t)
	header, _ := Signer{}.SignBody([]Key{{KID: "k", Material: []byte("secret")}}, body, ts)

	mkReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/webhooks/inbound/midnight-incoming/ep1", bytes.NewReader(body))
		req.Header.Set(SignatureHeader, header)
		req.Header.Set(DeliveryHeader, "whd_replay")
		req.Header.Set("Content-Type", cloudevents.ContentType)
		return req
	}

	rec1 := httptest.NewRecorder()
	receiver.HandleInbound(ProviderMidnightIncoming).ServeHTTP(rec1, mkReq())
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first call code = %d", rec1.Code)
	}
	rec2 := httptest.NewRecorder()
	receiver.HandleInbound(ProviderMidnightIncoming).ServeHTTP(rec2, mkReq())
	if rec2.Code != http.StatusOK {
		t.Fatalf("replay code = %d body=%s", rec2.Code, rec2.Body.String())
	}
	if rec2.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay header missing")
	}
	if got := dispatcher.snapshot(); len(got) != 1 {
		t.Fatalf("dispatcher got %d deliveries (replay should not redispatch)", len(got))
	}
	emits := audit.snapshot()
	var accepted, replayed int
	for _, e := range emits {
		switch e.Outcome {
		case "accepted":
			accepted++
		case "replayed":
			replayed++
		}
	}
	if accepted != 1 || replayed != 1 {
		t.Fatalf("audit emit accepted=%d replayed=%d", accepted, replayed)
	}
}

func TestReceiverRejectsBadSignature(t *testing.T) {
	ts := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	resolver := NewMemoryEndpointResolver()
	resolver.Set("ep1", EndpointConfig{Provider: ProviderMidnightIncoming, Keys: []Key{{KID: "k", Material: []byte("secret")}}})
	audit := &captureAudit{}
	receiver := NewReceiver(Options{Resolver: resolver, Clock: func() time.Time { return ts }, Audit: audit})
	_, body := validIncoming(t)
	header, _ := Signer{}.SignBody([]Key{{KID: "k", Material: []byte("WRONG-KEY")}}, body, ts)
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/webhooks/inbound/midnight-incoming/ep1", bytes.NewReader(body))
	req.Header.Set(SignatureHeader, header)
	req.Header.Set(DeliveryHeader, "whd_bad")
	req.Header.Set("Content-Type", cloudevents.ContentType)
	rec := httptest.NewRecorder()
	receiver.HandleInbound(ProviderMidnightIncoming).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error.Code != "WEBHOOK_SIGNATURE_INVALID" {
		t.Fatalf("error code = %s", resp.Error.Code)
	}
	if got := audit.snapshot(); len(got) != 1 || got[0].Outcome != "rejected_signature" {
		t.Fatalf("audit emit = %+v", got)
	}
}

func TestReceiverRejectsReplayWindowViolation(t *testing.T) {
	clockNow := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	resolver := NewMemoryEndpointResolver()
	resolver.Set("ep1", EndpointConfig{Provider: ProviderMidnightIncoming, Keys: []Key{{KID: "k", Material: []byte("secret")}}})
	audit := &captureAudit{}
	receiver := NewReceiver(Options{Resolver: resolver, Clock: func() time.Time { return clockNow }, Audit: audit})

	_, body := validIncoming(t)
	stale := clockNow.Add(-301 * time.Second)
	header, _ := Signer{}.SignBody([]Key{{KID: "k", Material: []byte("secret")}}, body, stale)
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/webhooks/inbound/midnight-incoming/ep1", bytes.NewReader(body))
	req.Header.Set(SignatureHeader, header)
	req.Header.Set(DeliveryHeader, "whd_stale")
	req.Header.Set("Content-Type", cloudevents.ContentType)
	rec := httptest.NewRecorder()
	receiver.HandleInbound(ProviderMidnightIncoming).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d", rec.Code)
	}
	if got := audit.snapshot(); len(got) != 1 || got[0].Outcome != "rejected_replay" {
		t.Fatalf("audit emit = %+v", got)
	}
}

func TestReceiverRejectsDisabledEndpoint(t *testing.T) {
	resolver := NewMemoryEndpointResolver()
	resolver.Set("ep1", EndpointConfig{Provider: ProviderGitHub, Disabled: true})
	audit := &captureAudit{}
	receiver := NewReceiver(Options{Resolver: resolver, Audit: audit, Clock: time.Now})
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/webhooks/inbound/github/ep1", bytes.NewReader([]byte("{}")))
	rec := httptest.NewRecorder()
	receiver.HandleInbound(ProviderGitHub).ServeHTTP(rec, req)
	if rec.Code != http.StatusGone {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestReceiverGitHubSignatureAccepted(t *testing.T) {
	ts := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	resolver := NewMemoryEndpointResolver()
	resolver.Set("ep1", EndpointConfig{Provider: ProviderGitHub, Keys: []Key{{KID: "ghk", Material: []byte("github-secret")}}})
	audit := &captureAudit{}
	dispatcher := &captureDispatcher{}
	receiver := NewReceiver(Options{Resolver: resolver, Audit: audit, Dispatcher: dispatcher, Clock: func() time.Time { return ts }})

	body := []byte(`{"action":"opened"}`)
	sig := "sha256=" + hmacSHA256Hex([]byte("github-secret"), body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/webhooks/inbound/github/ep1", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("X-GitHub-Delivery", "ghd_1")
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	receiver.HandleInbound(ProviderGitHub).ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("github code = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := dispatcher.snapshot(); len(got) != 1 || got[0].DeliveryID != "ghd_1" {
		t.Fatalf("dispatch = %+v", got)
	}
}

func TestReceiverGitHubDurableAdmissionResponsesAndNoSynchronousDispatch(t *testing.T) {
	resolver := NewMemoryEndpointResolver()
	key := Key{KID: "ghk_current", Material: []byte("github-secret")}
	resolver.Set("ep1", EndpointConfig{Provider: ProviderGitHub, Keys: []Key{key}})
	body := []byte(`{"ref":"refs/heads/main","after":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)

	tests := []struct {
		name             string
		result           AcceptResult
		err              error
		wantStatus       int
		wantReplayHeader string
		wantInboxHeader  string
	}{
		{
			name: "created",
			result: AcceptResult{
				ID:                 "wbd_created",
				ProviderDeliveryID: "delivery-durable",
				Disposition:        AcceptCreated,
			},
			wantStatus:      http.StatusAccepted,
			wantInboxHeader: "wbd_created",
		},
		{
			name: "replayed",
			result: AcceptResult{
				ID:                 "wbd_created",
				ProviderDeliveryID: "delivery-durable",
				Disposition:        AcceptReplayed,
			},
			wantStatus:       http.StatusOK,
			wantReplayHeader: "true",
			wantInboxHeader:  "wbd_created",
		},
		{
			name:       "identity conflict",
			err:        ErrDeliveryIdentityReuse,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "inbox unavailable",
			err:        errors.New("postgres unavailable"),
			wantStatus: http.StatusServiceUnavailable,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inbox := &captureInboxAcceptance{result: test.result, err: test.err}
			dispatcher := &captureDispatcher{}
			receiver := NewReceiver(Options{
				Resolver:   resolver,
				Inbox:      inbox,
				Dispatcher: dispatcher,
			})
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/v1alpha/webhooks/inbound/github/ep1",
				bytes.NewReader(body),
			)
			request.Header.Set(
				"X-Hub-Signature-256",
				"sha256="+hmacSHA256Hex(key.Material, body),
			)
			request.Header.Set("X-GitHub-Delivery", "delivery-durable")
			request.Header.Set("X-GitHub-Event", "push")
			response := httptest.NewRecorder()

			receiver.HandleInbound(ProviderGitHub).ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
			}
			if response.Header().Get("Idempotent-Replayed") != test.wantReplayHeader {
				t.Fatalf(
					"Idempotent-Replayed = %q, want %q",
					response.Header().Get("Idempotent-Replayed"),
					test.wantReplayHeader,
				)
			}
			if response.Header().Get("Midnight-Inbox-Id") != test.wantInboxHeader {
				t.Fatalf(
					"Midnight-Inbox-Id = %q, want %q",
					response.Header().Get("Midnight-Inbox-Id"),
					test.wantInboxHeader,
				)
			}
			if len(inbox.requests) != 1 {
				t.Fatalf("durable inbox received %d requests", len(inbox.requests))
			}
			got := inbox.requests[0]
			if got.Provider != ProviderGitHub ||
				got.EndpointID != "ep1" ||
				got.ProviderDeliveryID != "delivery-durable" ||
				got.EventType != "push" ||
				got.EventHash != PayloadHashSHA256(body) ||
				got.PayloadSHA256 != PayloadHashSHA256(body) ||
				got.SignatureKeyID != key.KID ||
				!bytes.Equal(got.Payload, body) {
				t.Fatalf("durable acceptance request = %#v", got)
			}
			if got := dispatcher.snapshot(); len(got) != 0 {
				t.Fatalf("durable admission synchronously dispatched %#v", got)
			}
		})
	}
}

func TestReceiverGenericDurableAdmissionDefaultsMissingEventType(t *testing.T) {
	now := time.Date(2026, 7, 29, 1, 2, 3, 0, time.UTC)
	key := Key{KID: "gitlab-current", Material: []byte("generic-secret")}
	resolver := NewMemoryEndpointResolver()
	resolver.Set("ep1", EndpointConfig{Provider: ProviderGitLab, Keys: []Key{key}})
	inbox := &captureInboxAcceptance{result: AcceptResult{
		ID:                 "wbd_generic",
		ProviderDeliveryID: "anon-placeholder",
		Disposition:        AcceptCreated,
	}}
	receiver := NewReceiver(Options{
		Resolver: resolver,
		Inbox:    inbox,
		Clock:    func() time.Time { return now },
	})
	body := []byte(`{"object_kind":"push"}`)
	signature, err := (Signer{}).SignBody([]Key{key}, body, now)
	if err != nil {
		t.Fatalf("sign body: %v", err)
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1alpha/webhooks/inbound/gitlab-generic/ep1",
		bytes.NewReader(body),
	)
	request.Header.Set(SignatureHeader, signature)
	response := httptest.NewRecorder()

	receiver.HandleInbound(ProviderGitLab).ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if len(inbox.requests) != 1 {
		t.Fatalf("durable inbox received %d requests", len(inbox.requests))
	}
	got := inbox.requests[0]
	if got.EventType != "unknown" {
		t.Fatalf("event type = %q, want unknown", got.EventType)
	}
	if got.ProviderDeliveryID != "anon-"+PayloadHashSHA256(body)[:16] {
		t.Fatalf("provider delivery id = %q", got.ProviderDeliveryID)
	}
}

func TestReceiverGitHubRequiresStandardIdentityHeaders(t *testing.T) {
	resolver := NewMemoryEndpointResolver()
	key := Key{KID: "ghk", Material: []byte("github-secret")}
	resolver.Set("ep1", EndpointConfig{Provider: ProviderGitHub, Keys: []Key{key}})
	dispatcher := &captureDispatcher{}
	receiver := NewReceiver(Options{Resolver: resolver, Dispatcher: dispatcher})
	body := []byte(`{"ref":"refs/heads/main"}`)

	newRequest := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/webhooks/inbound/github/ep1", bytes.NewReader(body))
		req.Header.Set("X-Hub-Signature-256", "sha256="+hmacSHA256Hex(key.Material, body))
		req.Header.Set("X-GitHub-Delivery", "01234567-89ab-cdef-0123-456789abcdef")
		req.Header.Set("X-GitHub-Event", "push")
		return req
	}

	tests := map[string]func(*http.Request){
		"missing signature with midnight alternative": func(req *http.Request) {
			req.Header.Del("X-Hub-Signature-256")
			req.Header.Set(SignatureHeader, "sha256="+hmacSHA256Hex(key.Material, body))
		},
		"duplicate signature": func(req *http.Request) {
			req.Header.Add("X-Hub-Signature-256", req.Header.Get("X-Hub-Signature-256"))
		},
		"malformed signature": func(req *http.Request) {
			req.Header.Set("X-Hub-Signature-256", "sha1="+strings.Repeat("0", 64))
		},
		"control in signature": func(req *http.Request) {
			req.Header.Set("X-Hub-Signature-256", "sha256="+strings.Repeat("0", 63)+"\x7f")
		},
		"missing delivery": func(req *http.Request) {
			req.Header.Del("X-GitHub-Delivery")
		},
		"duplicate delivery": func(req *http.Request) {
			req.Header.Add("X-GitHub-Delivery", "another-delivery")
		},
		"control in delivery": func(req *http.Request) {
			req.Header.Set("X-GitHub-Delivery", "delivery\x7f")
		},
		"missing event": func(req *http.Request) {
			req.Header.Del("X-GitHub-Event")
		},
		"duplicate event": func(req *http.Request) {
			req.Header.Add("X-GitHub-Event", "ping")
		},
		"malformed event": func(req *http.Request) {
			req.Header.Set("X-GitHub-Event", "push, ping")
		},
		"control in event": func(req *http.Request) {
			req.Header.Set("X-GitHub-Event", "push\t")
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			req := newRequest()
			mutate(req)
			rec := httptest.NewRecorder()
			receiver.HandleInbound(ProviderGitHub).ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
	if got := dispatcher.snapshot(); len(got) != 0 {
		t.Fatalf("malformed requests dispatched: %#v", got)
	}
}

func TestReceiverRejectsEndpointProviderMismatch(t *testing.T) {
	resolver := NewMemoryEndpointResolver()
	resolver.Set("ep1", EndpointConfig{Provider: ProviderMidnightIncoming, Keys: []Key{{Material: []byte("secret")}}})
	receiver := NewReceiver(Options{Resolver: resolver})
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/webhooks/inbound/github/ep1", bytes.NewReader([]byte("{}")))
	rec := httptest.NewRecorder()
	receiver.HandleInbound(ProviderGitHub).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestReceiverRejectsBodyOverflowWithoutDispatch(t *testing.T) {
	resolver := NewMemoryEndpointResolver()
	resolver.Set("ep1", EndpointConfig{Provider: ProviderGitHub, Keys: []Key{{Material: []byte("secret")}}})
	dispatcher := &captureDispatcher{}
	receiver := NewReceiver(Options{Resolver: resolver, Dispatcher: dispatcher})

	body := bytes.Repeat([]byte{'x'}, int(MaxInboundBodyBytes)+1)
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/webhooks/inbound/github/ep1", bytes.NewReader(body))
	// Exercise the bounded-reader overflow path rather than only the
	// Content-Length fast rejection.
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	receiver.HandleInbound(ProviderGitHub).ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := dispatcher.snapshot(); len(got) != 0 {
		t.Fatalf("oversize body dispatched: %#v", got)
	}
}
