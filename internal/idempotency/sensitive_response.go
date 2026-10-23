// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package idempotency

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/midnight/midnight/internal/secrets"
)

const sensitiveResponseAADDomain = "midnight/idempotency-sensitive-response/v1\x00"

type sealedResponse struct {
	Envelope  secrets.Envelope
	AADSHA256 string
}

type sensitiveResponseWire struct {
	StatusCode int         `json:"status_code"`
	Header     http.Header `json:"header"`
	Body       []byte      `json:"body"`
}

func sealSensitiveResponse(
	ctx context.Context,
	keys secrets.KeyProvider,
	ref Reference,
	state State,
	response StoredResponse,
) (sealedResponse, error) {
	if keys == nil {
		return sealedResponse{}, ErrSensitiveResponseUnavailable
	}
	aad := sensitiveResponseAAD(ref, state)
	wire, err := json.Marshal(sensitiveResponseWire{
		StatusCode: response.StatusCode,
		Header:     response.Header,
		Body:       response.Body,
	})
	if err != nil {
		return sealedResponse{}, ErrSensitiveResponseUnavailable
	}
	defer clearBytes(wire)
	envelope, err := secrets.SealForVariable(
		ctx,
		keys,
		secrets.VariableID(string(aad)),
		wire,
	)
	if err != nil {
		return sealedResponse{}, ErrSensitiveResponseUnavailable
	}
	return sealedResponse{
		Envelope:  envelope,
		AADSHA256: sha256Hex(aad),
	}, nil
}

func openSensitiveResponse(
	ctx context.Context,
	keys secrets.KeyProvider,
	ref Reference,
	state State,
	sealed sealedResponse,
) (StoredResponse, error) {
	if keys == nil {
		return StoredResponse{}, ErrSensitiveResponseUnavailable
	}
	aad := sensitiveResponseAAD(ref, state)
	if sealed.AADSHA256 == "" ||
		!equalDigest(sealed.AADSHA256, sha256Hex(aad)) {
		return StoredResponse{}, ErrSensitiveResponseUnavailable
	}
	wire, err := secrets.OpenForVariable(
		ctx,
		keys,
		secrets.VariableID(string(aad)),
		sealed.Envelope,
	)
	if err != nil {
		return StoredResponse{}, ErrSensitiveResponseUnavailable
	}
	defer clearBytes(wire)
	var decoded sensitiveResponseWire
	if err := json.Unmarshal(wire, &decoded); err != nil {
		return StoredResponse{}, ErrSensitiveResponseUnavailable
	}
	if decoded.StatusCode < 100 || decoded.StatusCode > 599 {
		clearBytes(decoded.Body)
		return StoredResponse{}, ErrSensitiveResponseUnavailable
	}
	response := StoredResponse{
		StatusCode: decoded.StatusCode,
		Header:     decoded.Header,
		Body:       append([]byte(nil), decoded.Body...),
	}
	clearBytes(decoded.Body)
	if err := validateTerminalResponse(state, response); err != nil {
		clearBytes(response.Body)
		return StoredResponse{}, ErrSensitiveResponseUnavailable
	}
	return response, nil
}

// sensitiveResponseAAD is a collision-free length-prefixed encoding of the
// complete immutable replay fence and terminal state. Tenant is the canonical
// workspace/project authorization scope selected by the coordinator. The
// envelope's KEKID binds the separate encryption-key generation through the
// secrets envelope.
func sensitiveResponseAAD(ref Reference, state State) []byte {
	fields := []string{
		string(ref.Actor),
		string(ref.Tenant),
		ref.Method,
		ref.Path,
		string(ref.Key),
		ref.BodySHA256,
		ref.Generation,
		string(state),
	}
	size := len(sensitiveResponseAADDomain) + len(fields)*4
	for _, field := range fields {
		size += len(field)
	}
	out := make([]byte, 0, size)
	out = append(out, sensitiveResponseAADDomain...)
	var length [4]byte
	for _, field := range fields {
		binary.BigEndian.PutUint32(length[:], uint32(len(field)))
		out = append(out, length[:]...)
		out = append(out, field...)
	}
	return out
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func equalDigest(left, right string) bool {
	leftBytes, leftErr := hex.DecodeString(left)
	rightBytes, rightErr := hex.DecodeString(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return len(leftBytes) == sha256.Size &&
		len(rightBytes) == sha256.Size &&
		subtle.ConstantTimeCompare(leftBytes, rightBytes) == 1
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func validateTerminalResponse(state State, response StoredResponse) error {
	if state == StateSucceeded {
		if response.StatusCode < http.StatusOK ||
			response.StatusCode >= http.StatusMultipleChoices {
			return fmt.Errorf(
				"%w: success response status must be 2xx",
				ErrReservationFenced,
			)
		}
		return nil
	}
	if state == StateFailed &&
		response.StatusCode >= http.StatusBadRequest &&
		response.StatusCode <= 599 {
		return nil
	}
	return fmt.Errorf(
		"%w: failure response status must be 4xx or 5xx",
		ErrReservationFenced,
	)
}
