// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/midnight/midnight/pkg/orailtypes"
)

const deploymentCursorPrefix = "dc1"

type deploymentCursor struct {
	ServiceID     string `json:"service_id"`
	EnvironmentID string `json:"environment_id,omitempty"`
	TruthSource   string `json:"truth_source"`
	CreatedAt     string `json:"created_at"`
	DeploymentID  string `json:"deployment_id"`
}

func (c CursorCodec) encodeDeploymentCursor(
	serviceID string,
	environmentID string,
	truthSource string,
	createdAt time.Time,
	deploymentID string,
) (string, error) {
	cursor := deploymentCursor{
		ServiceID:     strings.TrimSpace(serviceID),
		EnvironmentID: strings.TrimSpace(environmentID),
		TruthSource:   strings.TrimSpace(truthSource),
		CreatedAt:     createdAt.UTC().Format(time.RFC3339Nano),
		DeploymentID:  strings.TrimSpace(deploymentID),
	}
	if cursor.ServiceID == "" ||
		(cursor.TruthSource != deploymentTruthCanonical &&
			cursor.TruthSource != deploymentTruthLegacy) ||
		cursor.CreatedAt == "" || cursor.DeploymentID == "" ||
		len(c.secret) == 0 {
		return "", orailtypes.ErrInvalidCursorPage
	}
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return deploymentCursorPrefix + "." + encoded + "." + c.deploymentCursorSignature(encoded), nil
}

func (c CursorCodec) decodeDeploymentCursor(
	raw string,
	serviceID string,
	environmentID string,
) (time.Time, string, string, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, "", "", orailtypes.ErrInvalidCursorPage
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] != deploymentCursorPrefix || len(c.secret) == 0 ||
		!hmac.Equal(
			[]byte(parts[2]),
			[]byte(c.deploymentCursorSignature(parts[1])),
		) {
		return time.Time{}, "", "", orailtypes.ErrInvalidCursorPage
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, "", "", orailtypes.ErrInvalidCursorPage
	}
	var cursor deploymentCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return time.Time{}, "", "", orailtypes.ErrInvalidCursorPage
	}
	if cursor.ServiceID != strings.TrimSpace(serviceID) ||
		cursor.EnvironmentID != strings.TrimSpace(environmentID) ||
		(cursor.TruthSource != deploymentTruthCanonical &&
			cursor.TruthSource != deploymentTruthLegacy) ||
		strings.TrimSpace(cursor.DeploymentID) == "" {
		return time.Time{}, "", "", orailtypes.ErrInvalidCursorPage
	}
	createdAt, err := time.Parse(time.RFC3339Nano, cursor.CreatedAt)
	if err != nil || createdAt.IsZero() {
		return time.Time{}, "", "", orailtypes.ErrInvalidCursorPage
	}
	return createdAt.UTC(), cursor.DeploymentID, cursor.TruthSource, nil
}

func (c CursorCodec) deploymentCursorSignature(payload string) string {
	mac := hmac.New(sha256.New, c.secret)
	_, _ = mac.Write([]byte("midnight:deployment-cursor:v1:"))
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
