// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package monitors

import (
	"context"
	"errors"

	"github.com/midnight/midnight/internal/secrets"
)

// VaultSecretResolver opens a sealed variable only at the notification
// delivery boundary. The caller owns and must zero the returned copy.
type VaultSecretResolver struct {
	Vault *secrets.Vault
}

func (resolver VaultSecretResolver) ResolveSecret(ctx context.Context, id string) ([]byte, error) {
	if resolver.Vault == nil || id == "" {
		return nil, errors.New("monitor secret resolver is unavailable")
	}
	value, _, err := resolver.Vault.Decrypt(ctx, secrets.VariableID(id), secrets.ScopeNotificationDelivery)
	if err != nil {
		return nil, err
	}
	return value, nil
}

var _ SecretResolver = VaultSecretResolver{}
