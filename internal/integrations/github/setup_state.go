// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package github

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	SetupStateVersion = "github-install-v1"
	DefaultSetupTTL   = 10 * time.Minute
	setupNonceBytes   = 32
	maxSetupStateLen  = 4096
)

var ErrSetupStateInvalid = errors.New("github: setup state is invalid")

type SetupState struct {
	Version     string `json:"v"`
	Nonce       string `json:"n"`
	WorkspaceID string `json:"-"`
	UserID      string `json:"-"`
	SessionID   string `json:"-"`
	IssuedAt    int64  `json:"iat"`
	ExpiresAt   int64  `json:"exp"`
}

func (s SetupState) IssuedTime() time.Time  { return time.Unix(s.IssuedAt, 0).UTC() }
func (s SetupState) ExpiresTime() time.Time { return time.Unix(s.ExpiresAt, 0).UTC() }

func (s SetupState) NonceHash() []byte {
	sum := sha256.Sum256([]byte(s.Nonce))
	return append([]byte(nil), sum[:]...)
}

type SetupStateCodec struct {
	key    []byte
	now    func() time.Time
	random func([]byte) error
	ttl    time.Duration
}

type SetupStateOption func(*SetupStateCodec)

func WithSetupClock(now func() time.Time) SetupStateOption {
	return func(codec *SetupStateCodec) {
		if now != nil {
			codec.now = now
		}
	}
}

func WithSetupRandom(random func([]byte) error) SetupStateOption {
	return func(codec *SetupStateCodec) {
		if random != nil {
			codec.random = random
		}
	}
}

func WithSetupTTL(ttl time.Duration) SetupStateOption {
	return func(codec *SetupStateCodec) {
		if ttl > 0 && ttl <= time.Hour {
			codec.ttl = ttl
		}
	}
}

func NewSetupStateCodec(key []byte, options ...SetupStateOption) (*SetupStateCodec, error) {
	if len(key) < 32 {
		return nil, fmt.Errorf("%w: signing key must be at least 32 bytes", ErrSetupStateInvalid)
	}
	codec := &SetupStateCodec{
		key: append([]byte(nil), key...),
		now: func() time.Time {
			return time.Now().UTC()
		},
		random: func(dst []byte) error {
			_, err := rand.Read(dst)
			return err
		},
		ttl: DefaultSetupTTL,
	}
	for _, option := range options {
		option(codec)
	}
	return codec, nil
}

func (c *SetupStateCodec) Issue(workspaceID, userID, sessionID string) (string, SetupState, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	userID = strings.TrimSpace(userID)
	sessionID = strings.TrimSpace(sessionID)
	if c == nil || workspaceID == "" || userID == "" || sessionID == "" {
		return "", SetupState{}, ErrSetupStateInvalid
	}
	nonce := make([]byte, setupNonceBytes)
	if err := c.random(nonce); err != nil {
		return "", SetupState{}, fmt.Errorf("%w: random nonce: %v", ErrSetupStateInvalid, err)
	}
	now := c.now().UTC().Truncate(time.Second)
	state := SetupState{
		Version:     SetupStateVersion,
		Nonce:       base64.RawURLEncoding.EncodeToString(nonce),
		WorkspaceID: workspaceID,
		UserID:      userID,
		SessionID:   sessionID,
		IssuedAt:    now.Unix(),
		ExpiresAt:   now.Add(c.ttl).Unix(),
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return "", SetupState{}, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + base64.RawURLEncoding.EncodeToString(c.sign(encoded)), state, nil
}

func (c *SetupStateCodec) Verify(value string) (SetupState, error) {
	value = strings.TrimSpace(value)
	if c == nil || value == "" || len(value) > maxSetupStateLen {
		return SetupState{}, ErrSetupStateInvalid
	}
	payloadPart, signaturePart, ok := strings.Cut(value, ".")
	if !ok || payloadPart == "" || signaturePart == "" || strings.Contains(signaturePart, ".") {
		return SetupState{}, ErrSetupStateInvalid
	}
	signature, err := base64.RawURLEncoding.DecodeString(signaturePart)
	if err != nil || !hmac.Equal(signature, c.sign(payloadPart)) {
		return SetupState{}, ErrSetupStateInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadPart)
	if err != nil {
		return SetupState{}, ErrSetupStateInvalid
	}
	var state SetupState
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return SetupState{}, ErrSetupStateInvalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return SetupState{}, ErrSetupStateInvalid
	}
	now := c.now().UTC()
	if state.Version != SetupStateVersion ||
		state.IssuedAt <= 0 || state.ExpiresAt <= state.IssuedAt ||
		state.ExpiresTime().Sub(state.IssuedTime()) > time.Hour ||
		now.Before(state.IssuedTime().Add(-time.Minute)) || !now.Before(state.ExpiresTime()) {
		return SetupState{}, ErrSetupStateInvalid
	}
	nonce, err := base64.RawURLEncoding.DecodeString(state.Nonce)
	if err != nil || len(nonce) != setupNonceBytes {
		return SetupState{}, ErrSetupStateInvalid
	}
	return state, nil
}

func (c *SetupStateCodec) sign(payload string) []byte {
	mac := hmac.New(sha256.New, c.key)
	_, _ = mac.Write([]byte("midnight/github-app-setup/v1\x00"))
	_, _ = mac.Write([]byte(payload))
	return mac.Sum(nil)
}
