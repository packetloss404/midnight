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
	"strings"
	"time"

	"github.com/midnight/midnight/pkg/orailtypes"
)

type CursorTokenKind string

const (
	CursorTokenUnsigned CursorTokenKind = "unsigned"
	CursorTokenSigned   CursorTokenKind = "signed"
)

type CursorToken struct {
	Kind   CursorTokenKind
	Offset int
}

type cursorPayload struct {
	Offset int `json:"offset"`
}

const operationCursorTTL = 15 * time.Minute

type operationCursorPayload struct {
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
	Binding   string `json:"binding"`
	ExpiresAt int64  `json:"expires_at"`
}

type CursorCodec struct {
	secret []byte
}

func NewCursorCodec(secret []byte) CursorCodec {
	return CursorCodec{secret: append([]byte(nil), secret...)}
}

func (c CursorCodec) Encode(token CursorToken) (string, error) {
	if token.Offset < 0 {
		return "", orailtypes.ErrInvalidCursorPage
	}
	payload, err := json.Marshal(cursorPayload{Offset: token.Offset})
	if err != nil {
		return "", err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	if token.Kind == CursorTokenUnsigned && len(c.secret) != 0 {
		return "", orailtypes.ErrInvalidCursorPage
	}
	if len(c.secret) == 0 {
		return "u." + encodedPayload, nil
	}
	return "s." + encodedPayload + "." + c.signature(encodedPayload), nil
}

func (c CursorCodec) Decode(raw string) (CursorToken, error) {
	if raw == "" {
		return CursorToken{}, orailtypes.ErrInvalidCursorPage
	}
	parts := strings.Split(raw, ".")
	switch {
	case len(parts) == 2 && parts[0] == "u":
		if len(c.secret) != 0 {
			return CursorToken{}, orailtypes.ErrInvalidCursorPage
		}
		return decodeCursorPayload(CursorTokenUnsigned, parts[1])
	case len(parts) == 3 && parts[0] == "s":
		if len(c.secret) == 0 || !hmac.Equal([]byte(parts[2]), []byte(c.signature(parts[1]))) {
			return CursorToken{}, orailtypes.ErrInvalidCursorPage
		}
		return decodeCursorPayload(CursorTokenSigned, parts[1])
	default:
		return CursorToken{}, orailtypes.ErrInvalidCursorPage
	}
}

// EncodeOperationCursor emits a short-lived cursor bound to the canonical
// authenticated query. A configured signing key always produces a signed
// token; unsigned operation cursors exist only for explicit keyless developer
// stacks.
func (c CursorCodec) EncodeOperationCursor(position OperationListCursor, binding string, now time.Time) (string, error) {
	if position.CreatedAt.IsZero() || position.ID == "" || binding == "" {
		return "", orailtypes.ErrInvalidCursorPage
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	payload, err := json.Marshal(operationCursorPayload{
		CreatedAt: position.CreatedAt.UTC().Format(time.RFC3339Nano),
		ID:        string(position.ID),
		Binding:   binding,
		ExpiresAt: now.UTC().Add(operationCursorTTL).Unix(),
	})
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	if len(c.secret) == 0 {
		return "ou1." + encoded, nil
	}
	return "op1." + encoded + "." + c.operationSignature(encoded), nil
}

func (c CursorCodec) DecodeOperationCursor(raw, binding string, now time.Time) (OperationListCursor, error) {
	if raw == "" || binding == "" {
		return OperationListCursor{}, orailtypes.ErrInvalidCursorPage
	}
	parts := strings.Split(raw, ".")
	var encoded string
	switch {
	case len(parts) == 2 && parts[0] == "ou1" && len(c.secret) == 0:
		encoded = parts[1]
	case len(parts) == 3 && parts[0] == "op1" && len(c.secret) != 0 &&
		hmac.Equal([]byte(parts[2]), []byte(c.operationSignature(parts[1]))):
		encoded = parts[1]
	default:
		return OperationListCursor{}, orailtypes.ErrInvalidCursorPage
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return OperationListCursor{}, orailtypes.ErrInvalidCursorPage
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var decoded operationCursorPayload
	if err := decoder.Decode(&decoded); err != nil {
		return OperationListCursor{}, orailtypes.ErrInvalidCursorPage
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return OperationListCursor{}, orailtypes.ErrInvalidCursorPage
	}
	if !hmac.Equal([]byte(decoded.Binding), []byte(binding)) || decoded.ID == "" || decoded.ExpiresAt <= 0 {
		return OperationListCursor{}, orailtypes.ErrInvalidCursorPage
	}
	createdAt, err := time.Parse(time.RFC3339Nano, decoded.CreatedAt)
	if err != nil {
		return OperationListCursor{}, orailtypes.ErrInvalidCursorPage
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if !now.UTC().Before(time.Unix(decoded.ExpiresAt, 0).UTC()) {
		return OperationListCursor{}, orailtypes.ErrInvalidCursorPage
	}
	return OperationListCursor{CreatedAt: createdAt.UTC(), ID: orailtypes.ID(decoded.ID)}, nil
}

func decodeCursorPayload(kind CursorTokenKind, encoded string) (CursorToken, error) {
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return CursorToken{}, orailtypes.ErrInvalidCursorPage
	}
	var decoded cursorPayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return CursorToken{}, orailtypes.ErrInvalidCursorPage
	}
	if decoded.Offset < 0 {
		return CursorToken{}, orailtypes.ErrInvalidCursorPage
	}
	return CursorToken{Kind: kind, Offset: decoded.Offset}, nil
}

func (c CursorCodec) signature(payload string) string {
	mac := hmac.New(sha256.New, c.secret)
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (c CursorCodec) operationSignature(payload string) string {
	mac := hmac.New(sha256.New, c.secret)
	_, _ = mac.Write([]byte("midnight:operation-list-cursor:v1:"))
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func invalidCursorError(err error) error {
	if errors.Is(err, orailtypes.ErrInvalidCursorPage) {
		return err
	}
	return fmt.Errorf("%w: %v", orailtypes.ErrInvalidCursorPage, err)
}
