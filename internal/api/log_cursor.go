package api

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	logrecords "github.com/midnight/midnight/internal/logs"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const (
	logCursorPrefix  = "lc1."
	logCursorVersion = 1
	logCursorMaxSize = 4096
)

var logCursorAAD = []byte("midnight:log-cursor:v1")

type logCursorPayload struct {
	Version     int    `json:"v"`
	OperationID string `json:"operation_id"`
	Surface     string `json:"surface"`
	Query       string `json:"query,omitempty"`
	File        string `json:"file"`
	Offset      int    `json:"offset"`
}

// EncodeLogCursor encrypts and authenticates a file-store cursor while
// binding it to the operation, surface, and text filter that produced it.
// File paths are implementation details and never appear in the API token.
func (c CursorCodec) EncodeLogCursor(operationID, surface, query string, cursor logrecords.Cursor) (string, error) {
	payload := logCursorPayload{
		Version:     logCursorVersion,
		OperationID: strings.TrimSpace(operationID),
		Surface:     strings.TrimSpace(surface),
		Query:       strings.TrimSpace(query),
		File:        cursor.File,
		Offset:      cursor.Offset,
	}
	if payload.OperationID == "" || payload.Surface == "" || payload.File == "" || payload.Offset < 0 {
		return "", orailtypes.ErrInvalidCursorPage
	}
	plaintext, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal log cursor: %w", err)
	}
	aead, err := c.logCursorAEAD()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate log cursor nonce: %w", err)
	}
	sealed := aead.Seal(nonce, nonce, plaintext, logCursorAAD)
	return logCursorPrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// DecodeLogCursor authenticates and decrypts a cursor and rejects replay
// across operations, surfaces, or a changed text query.
func (c CursorCodec) DecodeLogCursor(raw, operationID, surface, query string) (logrecords.Cursor, error) {
	if !strings.HasPrefix(raw, logCursorPrefix) || len(raw) > logCursorMaxSize {
		return logrecords.Cursor{}, orailtypes.ErrInvalidCursorPage
	}
	sealed, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(raw, logCursorPrefix))
	if err != nil {
		return logrecords.Cursor{}, orailtypes.ErrInvalidCursorPage
	}
	aead, err := c.logCursorAEAD()
	if err != nil {
		return logrecords.Cursor{}, orailtypes.ErrInvalidCursorPage
	}
	if len(sealed) < aead.NonceSize()+aead.Overhead() {
		return logrecords.Cursor{}, orailtypes.ErrInvalidCursorPage
	}
	nonce, ciphertext := sealed[:aead.NonceSize()], sealed[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, ciphertext, logCursorAAD)
	if err != nil {
		return logrecords.Cursor{}, orailtypes.ErrInvalidCursorPage
	}
	var payload logCursorPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return logrecords.Cursor{}, orailtypes.ErrInvalidCursorPage
	}
	if payload.Version != logCursorVersion ||
		payload.OperationID != strings.TrimSpace(operationID) ||
		payload.Surface != strings.TrimSpace(surface) ||
		payload.Query != strings.TrimSpace(query) ||
		payload.File == "" ||
		payload.Offset < 0 {
		return logrecords.Cursor{}, orailtypes.ErrInvalidCursorPage
	}
	return logrecords.Cursor{File: payload.File, Offset: payload.Offset}, nil
}

func (c CursorCodec) logCursorAEAD() (cipher.AEAD, error) {
	if len(c.secret) == 0 {
		return nil, errors.New("log cursor secret is unavailable")
	}
	derived := sha256.Sum256(append([]byte("midnight:log-cursor:key:v1\x00"), c.secret...))
	block, err := aes.NewCipher(derived[:])
	if err != nil {
		return nil, fmt.Errorf("create log cursor cipher: %w", err)
	}
	return cipher.NewGCM(block)
}
