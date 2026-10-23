package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/agent/conversation"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const agentConversationCursorTTL = 15 * time.Minute

type agentConversationCursorPayload struct {
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
	Binding   string `json:"binding"`
	ExpiresAt int64  `json:"expires_at"`
}

type agentMessageCursorPayload struct {
	Seq       int64  `json:"seq"`
	Binding   string `json:"binding"`
	ExpiresAt int64  `json:"expires_at"`
}

func (c CursorCodec) encodeAgentConversationCursor(position conversation.ConversationListPosition, binding string, now time.Time) (string, error) {
	if position.CreatedAt.IsZero() || strings.TrimSpace(position.ID) == "" || binding == "" {
		return "", orailtypes.ErrInvalidCursorPage
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	payload, err := json.Marshal(agentConversationCursorPayload{
		CreatedAt: position.CreatedAt.UTC().Format(time.RFC3339Nano),
		ID:        position.ID,
		Binding:   binding,
		ExpiresAt: now.UTC().Add(agentConversationCursorTTL).Unix(),
	})
	if err != nil {
		return "", err
	}
	return c.encodeAgentCursor("ac1", "acu1", "conversation-list", payload)
}

func (c CursorCodec) decodeAgentConversationCursor(raw, binding string, now time.Time) (conversation.ConversationListPosition, error) {
	payload, err := c.decodeAgentCursor(raw, binding, now, "ac1", "acu1", "conversation-list")
	if err != nil {
		return conversation.ConversationListPosition{}, err
	}
	var decoded agentConversationCursorPayload
	if err := decodeStrictCursorJSON(payload, &decoded); err != nil || decoded.ID == "" {
		return conversation.ConversationListPosition{}, orailtypes.ErrInvalidCursorPage
	}
	createdAt, err := time.Parse(time.RFC3339Nano, decoded.CreatedAt)
	if err != nil || createdAt.IsZero() {
		return conversation.ConversationListPosition{}, orailtypes.ErrInvalidCursorPage
	}
	return conversation.ConversationListPosition{CreatedAt: createdAt.UTC(), ID: decoded.ID}, nil
}

func (c CursorCodec) encodeAgentMessageCursor(seq int64, binding string, now time.Time) (string, error) {
	if seq <= 0 || binding == "" {
		return "", orailtypes.ErrInvalidCursorPage
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	payload, err := json.Marshal(agentMessageCursorPayload{Seq: seq, Binding: binding, ExpiresAt: now.UTC().Add(agentConversationCursorTTL).Unix()})
	if err != nil {
		return "", err
	}
	return c.encodeAgentCursor("am1", "amu1", "message-list", payload)
}

func (c CursorCodec) decodeAgentMessageCursor(raw, binding string, now time.Time) (int64, error) {
	payload, err := c.decodeAgentCursor(raw, binding, now, "am1", "amu1", "message-list")
	if err != nil {
		return 0, err
	}
	var decoded agentMessageCursorPayload
	if err := decodeStrictCursorJSON(payload, &decoded); err != nil || decoded.Seq <= 0 {
		return 0, orailtypes.ErrInvalidCursorPage
	}
	return decoded.Seq, nil
}

func (c CursorCodec) encodeAgentCursor(signedPrefix, unsignedPrefix, domain string, payload []byte) (string, error) {
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	if len(c.secret) == 0 {
		// Explicit keyless developer stacks retain unsigned cursor support.
		return unsignedPrefix + "." + encoded, nil
	}
	return signedPrefix + "." + encoded + "." + c.agentCursorSignature(domain, encoded), nil
}

func (c CursorCodec) decodeAgentCursor(raw, binding string, now time.Time, signedPrefix, unsignedPrefix, domain string) ([]byte, error) {
	if strings.TrimSpace(raw) == "" || binding == "" {
		return nil, orailtypes.ErrInvalidCursorPage
	}
	parts := strings.Split(raw, ".")
	var encoded string
	switch {
	case len(parts) == 2 && parts[0] == unsignedPrefix && len(c.secret) == 0:
		encoded = parts[1]
	case len(parts) == 3 && parts[0] == signedPrefix && len(c.secret) != 0 &&
		hmac.Equal([]byte(parts[2]), []byte(c.agentCursorSignature(domain, parts[1]))):
		encoded = parts[1]
	default:
		return nil, orailtypes.ErrInvalidCursorPage
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, orailtypes.ErrInvalidCursorPage
	}
	var common struct {
		Binding   string `json:"binding"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(payload, &common); err != nil || common.ExpiresAt <= 0 ||
		!hmac.Equal([]byte(common.Binding), []byte(binding)) {
		return nil, orailtypes.ErrInvalidCursorPage
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if !now.UTC().Before(time.Unix(common.ExpiresAt, 0).UTC()) {
		return nil, orailtypes.ErrInvalidCursorPage
	}
	return payload, nil
}

func (c CursorCodec) agentCursorSignature(domain, payload string) string {
	mac := hmac.New(sha256.New, c.secret)
	_, _ = mac.Write([]byte("midnight:agent-" + domain + "-cursor:v1:"))
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func decodeStrictCursorJSON(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return orailtypes.ErrInvalidCursorPage
	}
	return nil
}

func agentConversationListBinding(r *http.Request, actor string, filter conversation.ListFilter, order string, limit int) string {
	return agentCollectionBinding(r, actor, "conversations", filter.ProjectID, filter.ServiceID, filter.ActorID, order, limit)
}

func agentMessageListBinding(r *http.Request, actor, conversationID, order string, limit int) string {
	return agentCollectionBinding(r, actor, "messages:"+conversationID, "", "", "", order, limit)
}

func agentCollectionBinding(r *http.Request, actor, collection, projectID, serviceID, actorFilter, order string, limit int) string {
	raw := fmt.Sprintf(
		"collection=%s\x00principal=%s\x00actor=%s\x00tenant=%s\x00project=%s\x00service=%s\x00actor_filter=%s\x00order=%s\x00limit=%d",
		collection,
		actor,
		idempotency.ActorFromContext(r.Context()),
		idempotency.TenantFromContext(r.Context()),
		projectID,
		serviceID,
		actorFilter,
		order,
		limit,
	)
	digest := sha256.Sum256([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}
