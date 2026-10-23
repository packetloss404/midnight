package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/cloudevents"
	"github.com/midnight/midnight/internal/failurecodes"
)

// Provider identifies the inbound source of a delivery.
type Provider string

const (
	// ProviderGitHub is the GitHub App / GitHub legacy webhook source.
	ProviderGitHub Provider = "github"
	// ProviderGitLab is the GitLab generic webhook source.
	ProviderGitLab Provider = "gitlab-generic"
	// ProviderBitbucket is the Bitbucket webhook source.
	ProviderBitbucket Provider = "bitbucket"
	// ProviderMidnightIncoming is the panel-locked generic CI source
	// (Sprint 10 WH-1008) that uses the same Midnight-Signature
	// envelope as outbound.
	ProviderMidnightIncoming Provider = "midnight-incoming"

	// MaxInboundBodyBytes is the maximum webhook request body retained in
	// memory. GitHub documents a 25 MiB webhook payload cap; reading one byte
	// beyond the boundary lets the receiver reject oversize payloads instead
	// of silently authenticating and dispatching a truncated body.
	MaxInboundBodyBytes int64 = 25 << 20
)

// EndpointConfig is the per-endpoint configuration the receiver looks
// up by URL path. Lane 1 will eventually persist these in
// webhook_endpoints; Lane 4 ships an in-memory map.
type EndpointConfig struct {
	Provider Provider
	// Keys is the active key set for the endpoint. The first entry is
	// the active (preferred) key; the second is the previous key
	// during the panel-locked 30-day rotation overlap.
	Keys []Key
	// Disabled rejects deliveries with 410 Gone when set (panel-
	// locked WEBHOOK_ENDPOINT_DISABLED state).
	Disabled bool
}

// EndpointResolver returns the EndpointConfig for the supplied path
// suffix (i.e. the {endpoint_id} component of the URL).
type EndpointResolver interface {
	Resolve(ctx context.Context, endpointID string) (EndpointConfig, error)
}

// ErrEndpointUnknown is returned when EndpointResolver does not have an
// EndpointConfig for the supplied id.
var ErrEndpointUnknown = errors.New("webhook: endpoint unknown")

// MemoryEndpointResolver is the in-memory EndpointResolver Lane 4 ships.
type MemoryEndpointResolver struct {
	data map[string]EndpointConfig
}

// NewMemoryEndpointResolver constructs a fresh resolver.
func NewMemoryEndpointResolver() *MemoryEndpointResolver {
	return &MemoryEndpointResolver{data: map[string]EndpointConfig{}}
}

// Set sets the EndpointConfig for the supplied endpoint id.
func (r *MemoryEndpointResolver) Set(id string, cfg EndpointConfig) {
	r.data[id] = cfg
}

// Resolve implements EndpointResolver.
func (r *MemoryEndpointResolver) Resolve(_ context.Context, id string) (EndpointConfig, error) {
	cfg, ok := r.data[id]
	if !ok {
		return EndpointConfig{}, ErrEndpointUnknown
	}
	return cfg, nil
}

// AuditSink is the panel-locked emit boundary the receiver uses to
// describe each accepted/rejected delivery. Lane 4 deliberately keeps
// this thin — Lane 1 wires it to the audit hash chain.
type AuditSink interface {
	EmitDelivery(ctx context.Context, ev DeliveryAudit)
}

// DeliveryAudit is the redaction-safe description of a single inbound
// delivery. Plaintext body NEVER appears here.
type DeliveryAudit struct {
	Provider   Provider
	EndpointID string
	DeliveryID string
	EventHash  string
	EventType  string
	EventID    string
	KID        string
	Outcome    string // "accepted" | "replayed" | "rejected_signature" | "rejected_replay" | "rejected_signature_malformed" | "rejected_endpoint_disabled"
	ReceivedAt time.Time
	ReasonCode string
}

// ReceivedDelivery is the structured outcome the receiver returns to a
// downstream handler that promotes the delivery to a deploy operation.
type ReceivedDelivery struct {
	Provider   Provider
	EndpointID string
	DeliveryID string
	EventHash  string
	// EventType is the upstream-supplied event-type token (e.g.
	// "push", "ping" for ProviderGitHub; ":type:" for CloudEvents
	// providers). The github webhook dispatcher routes off this value.
	EventType  string
	Body       []byte
	Event      cloudevents.Event // populated only for ProviderMidnightIncoming + structured CloudEvents
	ReceivedAt time.Time
}

// Receiver verifies and dedupes inbound webhook deliveries. The
// receiver is HTTP-aware: it returns an http.Handler that callers
// register on the panel-locked URL path
// /api/v1alpha/webhooks/inbound/{provider}/{endpoint_id}.
type Receiver struct {
	resolver EndpointResolver
	dedupe   DedupeStore
	inbox    DurableInbox
	clock    func() time.Time
	audit    AuditSink
	handler  Dispatcher
}

// Dispatcher is the downstream the receiver hands a verified delivery
// to. Lane 4 ships a no-op implementation; Lane 1's API handlers wire a
// real one that creates a deploy operation via the API client.
type Dispatcher interface {
	Dispatch(ctx context.Context, d ReceivedDelivery) error
}

// NoopDispatcher is the default Dispatcher.
type NoopDispatcher struct{}

// Dispatch implements Dispatcher.
func (NoopDispatcher) Dispatch(_ context.Context, _ ReceivedDelivery) error { return nil }

// Options bundles the caller-supplied dependencies.
type Options struct {
	Resolver   EndpointResolver
	Dedupe     DedupeStore
	Clock      func() time.Time
	Audit      AuditSink
	Dispatcher Dispatcher
	// Inbox enables PostgreSQL persist-before-ack delivery. When present, the
	// receiver never calls Dispatcher synchronously. Memory mode intentionally
	// leaves it nil and retains the explicitly non-durable compatibility path.
	Inbox DurableInbox
}

// NewReceiver constructs a Receiver.
func NewReceiver(opts Options) *Receiver {
	if opts.Dedupe == nil {
		opts.Dedupe = NewMemoryDedupeStore()
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.Audit == nil {
		opts.Audit = noopAudit{}
	}
	if opts.Dispatcher == nil {
		opts.Dispatcher = NoopDispatcher{}
	}
	return &Receiver{
		resolver: opts.Resolver,
		dedupe:   opts.Dedupe,
		inbox:    opts.Inbox,
		clock:    opts.Clock,
		audit:    opts.Audit,
		handler:  opts.Dispatcher,
	}
}

type noopAudit struct{}

func (noopAudit) EmitDelivery(context.Context, DeliveryAudit) {}

// HandleInbound is the http.Handler shape registered on the inbound
// path. The path components after /webhooks/inbound/{provider}/ are
// treated as the endpoint id.
func (r *Receiver) HandleInbound(provider Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			r.writeError(w, http.StatusMethodNotAllowed, string(failurecodes.WebhookMethodNotAllowed), "webhook deliveries require POST")
			return
		}
		ctx := req.Context()
		endpointID := strings.TrimPrefix(req.URL.Path, "/api/v1alpha/webhooks/inbound/"+string(provider)+"/")
		endpointID = strings.Trim(endpointID, "/")
		if endpointID == "" {
			r.writeError(w, http.StatusNotFound, "WEBHOOK_ENDPOINT_UNKNOWN", "endpoint id missing")
			return
		}
		cfg, err := r.resolver.Resolve(ctx, endpointID)
		if err != nil {
			r.writeError(w, http.StatusNotFound, "WEBHOOK_ENDPOINT_UNKNOWN", "endpoint unknown")
			return
		}
		if cfg.Provider != provider {
			r.writeError(w, http.StatusNotFound, "WEBHOOK_ENDPOINT_PROVIDER_MISMATCH", "endpoint unknown for provider")
			return
		}
		if cfg.Disabled {
			r.audit.EmitDelivery(ctx, DeliveryAudit{Provider: provider, EndpointID: endpointID, Outcome: "rejected_endpoint_disabled", ReceivedAt: r.clock(), ReasonCode: "WEBHOOK_ENDPOINT_DISABLED"})
			r.writeError(w, http.StatusGone, "WEBHOOK_ENDPOINT_DISABLED", "endpoint disabled")
			return
		}

		if req.ContentLength > MaxInboundBodyBytes {
			r.writeError(w, http.StatusRequestEntityTooLarge, "WEBHOOK_BODY_TOO_LARGE", "webhook body exceeds 25 MiB")
			return
		}
		body, err := io.ReadAll(io.LimitReader(req.Body, MaxInboundBodyBytes+1))
		if err != nil {
			r.writeError(w, http.StatusBadRequest, "WEBHOOK_BODY_READ_FAILED", "failed to read body")
			return
		}
		if int64(len(body)) > MaxInboundBodyBytes {
			r.writeError(w, http.StatusRequestEntityTooLarge, "WEBHOOK_BODY_TOO_LARGE", "webhook body exceeds 25 MiB")
			return
		}

		var deliveryID, eventType, signatureKeyID string
		if provider == ProviderGitHub {
			ghSig, headerErr := requiredSingleHeader(req.Header, "X-Hub-Signature-256", validGitHubSignatureHeader)
			if headerErr != nil {
				r.audit.EmitDelivery(ctx, DeliveryAudit{Provider: provider, EndpointID: endpointID, Outcome: "rejected_signature_malformed", ReceivedAt: r.clock(), ReasonCode: "WEBHOOK_SIGNATURE_INVALID"})
				r.writeError(w, http.StatusBadRequest, "WEBHOOK_SIGNATURE_INVALID", "X-Hub-Signature-256 missing or malformed")
				return
			}
			signatureKeyID, err = VerifyGitHubSignatureKey(ghSig, cfg.Keys, body)
			if err != nil {
				r.audit.EmitDelivery(ctx, DeliveryAudit{Provider: provider, EndpointID: endpointID, Outcome: "rejected_signature", ReceivedAt: r.clock(), ReasonCode: "WEBHOOK_SIGNATURE_INVALID"})
				r.writeError(w, http.StatusUnauthorized, "WEBHOOK_SIGNATURE_INVALID", "github signature invalid")
				return
			}
			deliveryID, headerErr = requiredSingleHeader(req.Header, "X-GitHub-Delivery", validGitHubDeliveryHeader)
			if headerErr != nil {
				r.writeError(w, http.StatusBadRequest, "WEBHOOK_DELIVERY_ID_INVALID", "X-GitHub-Delivery missing or malformed")
				return
			}
			eventType, headerErr = requiredSingleHeader(req.Header, "X-GitHub-Event", validGitHubEventHeader)
			if headerErr != nil {
				r.writeError(w, http.StatusBadRequest, "WEBHOOK_EVENT_TYPE_INVALID", "X-GitHub-Event missing or malformed")
				return
			}
		} else {
			signatureHeader := req.Header.Get(SignatureHeader)
			parsed, err := ParseSignature(signatureHeader)
			if err != nil {
				r.audit.EmitDelivery(ctx, DeliveryAudit{Provider: provider, EndpointID: endpointID, Outcome: "rejected_signature_malformed", ReceivedAt: r.clock(), ReasonCode: "WEBHOOK_SIGNATURE_INVALID"})
				r.writeError(w, http.StatusBadRequest, "WEBHOOK_SIGNATURE_INVALID", "signature malformed")
				return
			}
			if err := VerifySignature(parsed, cfg.Keys, body, r.clock()); err != nil {
				outcome := "rejected_signature"
				code := "WEBHOOK_SIGNATURE_INVALID"
				if errors.Is(err, ErrReplayRejected) {
					outcome = "rejected_replay"
					code = "WEBHOOK_REPLAY_REJECTED"
				}
				r.audit.EmitDelivery(ctx, DeliveryAudit{Provider: provider, EndpointID: endpointID, Outcome: outcome, ReceivedAt: r.clock(), ReasonCode: code, KID: parsed.KID})
				status := http.StatusUnauthorized
				if errors.Is(err, ErrReplayRejected) {
					status = http.StatusBadRequest
				}
				r.writeError(w, status, code, err.Error())
				return
			}
			signatureKeyID = parsed.KID
			deliveryID = req.Header.Get(DeliveryHeader)
			eventType = req.Header.Get(EventTypeHeader)
		}

		eventID := req.Header.Get(EventIDHeader)

		eventHash := ""
		var ev cloudevents.Event
		if provider == ProviderMidnightIncoming || strings.EqualFold(req.Header.Get("Content-Type"), cloudevents.ContentType) {
			parsed, err := cloudevents.Decode(body)
			if err == nil {
				ev = parsed
				eventHash, _ = cloudevents.Hash(parsed)
				if eventID == "" {
					eventID = parsed.ID
				}
				if eventType == "" {
					eventType = string(parsed.Type)
				}
			}
		}
		if eventHash == "" {
			eventHash = bodyHash(body)
		}
		if eventType == "" {
			// Generic providers are allowed to omit Midnight's advisory event
			// header. Preserve a valid durable envelope without inventing
			// provider-specific semantics; planners can deliberately ignore it.
			eventType = "unknown"
		}
		if deliveryID == "" {
			// Fall back to the event hash so dedupe still works for
			// providers that omit a delivery id header.
			deliveryID = "anon-" + eventHash[:16]
		}

		now := r.clock()
		delivery := ReceivedDelivery{
			Provider:   provider,
			EndpointID: endpointID,
			DeliveryID: deliveryID,
			EventHash:  eventHash,
			EventType:  eventType,
			Body:       body,
			Event:      ev,
			ReceivedAt: now,
		}
		if r.inbox != nil {
			accepted, err := r.inbox.Accept(ctx, InboxAcceptance{
				Provider:           provider,
				EndpointID:         endpointID,
				ProviderDeliveryID: deliveryID,
				EventType:          eventType,
				EventID:            eventID,
				EventHash:          eventHash,
				PayloadSHA256:      PayloadHashSHA256(body),
				Payload:            body,
				SignatureKeyID:     signatureKeyID,
			})
			if errors.Is(err, ErrDeliveryIdentityReuse) {
				r.writeError(w, http.StatusConflict, "WEBHOOK_DELIVERY_IDENTITY_MISMATCH", "delivery identity was already used with a different payload")
				return
			}
			if err != nil {
				r.writeError(w, http.StatusServiceUnavailable, "WEBHOOK_INBOX_UNAVAILABLE", "durable webhook admission failed")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Midnight-Delivery-Id", accepted.ProviderDeliveryID)
			w.Header().Set("Midnight-Inbox-Id", accepted.ID)
			if accepted.Disposition == AcceptReplayed {
				w.Header().Set("Idempotent-Replayed", "true")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("{\"ok\":true,\"replayed\":true}\n"))
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte("{\"ok\":true,\"replayed\":false}\n"))
			return
		}

		stored, replayed, err := r.dedupe.Reserve(ctx, DedupeRecord{
			DeliveryID: deliveryID,
			EventHash:  eventHash,
			Provider:   string(provider),
			ReceivedAt: r.clock(),
			Endpoint:   endpointID,
		})
		if err != nil {
			r.writeError(w, http.StatusInternalServerError, "WEBHOOK_DEDUPE_FAILED", "dedupe store failed")
			return
		}
		if replayed {
			w.Header().Set("Idempotent-Replayed", "true")
			w.Header().Set("Midnight-Delivery-Id", stored.DeliveryID)
			r.audit.EmitDelivery(ctx, DeliveryAudit{
				Provider:   provider,
				EndpointID: endpointID,
				DeliveryID: stored.DeliveryID,
				EventHash:  stored.EventHash,
				EventID:    eventID,
				EventType:  eventType,
				Outcome:    "replayed",
				ReceivedAt: r.clock(),
			})
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{\"ok\":true,\"replayed\":true}\n"))
			return
		}

		if err := r.handler.Dispatch(ctx, delivery); err != nil {
			r.writeError(w, http.StatusInternalServerError, "WEBHOOK_DISPATCH_FAILED", err.Error())
			return
		}
		r.audit.EmitDelivery(ctx, DeliveryAudit{
			Provider:   provider,
			EndpointID: endpointID,
			DeliveryID: deliveryID,
			EventHash:  eventHash,
			EventID:    eventID,
			EventType:  eventType,
			Outcome:    "accepted",
			ReceivedAt: now,
		})
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("{\"ok\":true,\"replayed\":false}\n"))
	}
}

func (r *Receiver) writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	}
	out, _ := json.Marshal(body)
	_, _ = w.Write(out)
}

func bodyHash(body []byte) string {
	// Fall back to a SHA-256 hash of the raw body for non-CloudEvents
	// payloads (e.g. GitHub push events). Hash is hex-encoded so it's
	// safe to put in audit + headers.
	return cloudeventsBodyHash(body)
}

// cloudeventsBodyHash is exported via a thin wrapper over crypto/sha256
// so we don't duplicate the import in this file.
func cloudeventsBodyHash(body []byte) string {
	return sha256Hex(body)
}

// VerifyGitHubSignature checks an X-Hub-Signature-256 header against the
// supplied keys. GitHub's contract uses HMAC-SHA256 over the raw body
// only (no timestamp). The first key whose HMAC matches is accepted; the
// rotation-overlap key set is the same panel-locked shape Lane 4 uses
// for outbound signing.
func VerifyGitHubSignature(header string, keys []Key, body []byte) error {
	_, err := VerifyGitHubSignatureKey(header, keys, body)
	return err
}

// VerifyGitHubSignatureKey returns the identifier of the exact active or
// overlap key that authenticated the raw payload. Key material and signature
// bytes are never retained.
func VerifyGitHubSignatureKey(header string, keys []Key, body []byte) (string, error) {
	const prefix = "sha256="
	if !validGitHubSignatureHeader(header) {
		return "", ErrSignatureMalformed
	}
	candidate, err := hex.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil || len(candidate) != sha256.Size {
		return "", ErrSignatureMalformed
	}
	for _, k := range keys {
		mac := hmac.New(sha256.New, k.Material)
		_, _ = mac.Write(body)
		if hmac.Equal(mac.Sum(nil), candidate) {
			return k.KID, nil
		}
	}
	return "", ErrSignatureMismatch
}

func requiredSingleHeader(header http.Header, name string, valid func(string) bool) (string, error) {
	var values []string
	for key, current := range header {
		if strings.EqualFold(key, name) {
			values = append(values, current...)
		}
	}
	if len(values) != 1 || !valid(values[0]) {
		return "", ErrSignatureMalformed
	}
	return values[0], nil
}

func validGitHubSignatureHeader(value string) bool {
	const prefix = "sha256="
	if value != strings.TrimSpace(value) || containsHeaderControl(value) || !strings.HasPrefix(value, prefix) {
		return false
	}
	encoded := strings.TrimPrefix(value, prefix)
	if len(encoded) != 2*sha256.Size {
		return false
	}
	_, err := hex.DecodeString(encoded)
	return err == nil
}

func validGitHubDeliveryHeader(value string) bool {
	if value == "" || len(value) > 128 || value != strings.TrimSpace(value) || containsHeaderControl(value) {
		return false
	}
	for _, c := range value {
		if !((c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == ':') {
			return false
		}
	}
	return true
}

func validGitHubEventHeader(value string) bool {
	if value == "" || len(value) > 64 || value != strings.TrimSpace(value) || containsHeaderControl(value) {
		return false
	}
	for _, c := range value {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}

func containsHeaderControl(value string) bool {
	for _, c := range value {
		if c < 0x20 || c == 0x7f {
			return true
		}
	}
	return false
}
