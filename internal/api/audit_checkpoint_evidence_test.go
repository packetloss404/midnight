// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/midnight/midnight/internal/audit"
)

func TestAuditEvidenceVerifiesOnlyOperatorConfiguredPublicKeys(t *testing.T) {
	chain := audit.NewMemoryChain()
	if _, err := chain.Append(context.Background(), audit.Row{ActorType: "system", ResourceKind: "audit", Action: "fixture"}); err != nil {
		t.Fatal(err)
	}
	key, err := audit.GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := audit.NewCheckpointer(chain, key).Sign(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, trusted := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing-trust", true: "pinned-public-key"}[trusted], func(t *testing.T) {
			stack := DataStack{AuditChain: chain}
			if trusted {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "pinned.pem"), audit.EncodeVerifyingKeyPEM(key), 0o644); err != nil {
					t.Fatal(err)
				}
				stack.AuditVerifyingKeys = audit.DirectoryKeyring{Path: dir}
			}
			router := NewRouterWithDataStack(stack)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1alpha/audit/evidence", nil))
			if response.Code != http.StatusOK {
				t.Fatalf("evidence status=%d body=%s", response.Code, response.Body.String())
			}
			var evidence audit.Evidence
			if err := json.Unmarshal(response.Body.Bytes(), &evidence); err != nil {
				t.Fatal(err)
			}
			if evidence.CheckpointCount != 1 || evidence.Verification == nil {
				t.Fatalf("checkpoint evidence omitted: %#v", evidence)
			}
			if trusted {
				if evidence.Status != "ready" || !evidence.Verification.Valid || evidence.Verification.SignatureStatus != "verified" {
					t.Fatalf("trusted evidence=%#v verification=%#v", evidence, evidence.Verification)
				}
			} else if evidence.Status != "needs-review" || evidence.Verification.Valid || evidence.Verification.Outcome != audit.OutcomeSignatureBreak {
				t.Fatalf("untrusted evidence claimed ready=%#v", evidence)
			}
			// Download remains raw evidence; it must not include or auto-trust keys.
			export := httptest.NewRecorder()
			router.ServeHTTP(export, httptest.NewRequest(http.MethodGet, "/api/v1alpha/audit/export", nil))
			if export.Code != http.StatusOK {
				t.Fatalf("export status=%d", export.Code)
			}
		})
	}
}

type retainedAuditCheckpointChain struct {
	audit.AuditChain
	checkpoints []audit.Checkpoint
}

func (c retainedAuditCheckpointChain) Snapshot(context.Context) (audit.Snapshot, error) {
	return audit.Snapshot{Checkpoints: c.checkpoints}, nil
}

type unavailableAuditTrust struct{}

func (unavailableAuditTrust) VerifyingKeys(context.Context) ([]audit.VerifyingKey, error) {
	return nil, errors.New("configured public key trust unavailable")
}

func TestAuditEvidenceDoesNotHideMissingRowsOrTrustFailures(t *testing.T) {
	chain := audit.NewMemoryChain()
	_, _ = chain.Append(context.Background(), audit.Row{ActorType: "system", ResourceKind: "audit", Action: "fixture"})
	key, _ := audit.GenerateSigningKey()
	cp, err := audit.NewCheckpointer(chain, key).Sign(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	trustDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(trustDir, "pinned.pem"), audit.EncodeVerifyingKeyPEM(key), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		chain   audit.AuditChain
		trust   audit.VerifyingKeyProvider
		outcome audit.VerifyOutcome
	}{
		{name: "retained-checkpoint-with-no-rows", chain: retainedAuditCheckpointChain{AuditChain: chain, checkpoints: []audit.Checkpoint{cp}}, trust: audit.DirectoryKeyring{Path: trustDir}, outcome: audit.OutcomeChainBreak},
		{name: "empty-history-unavailable-trust", chain: audit.NewMemoryChain(), trust: unavailableAuditTrust{}, outcome: audit.OutcomeSourceError},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := NewRouterWithDataStack(DataStack{AuditChain: test.chain, AuditVerifyingKeys: test.trust})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1alpha/audit/evidence", nil))
			if response.Code != http.StatusOK {
				t.Fatalf("evidence status=%d body=%s", response.Code, response.Body.String())
			}
			var evidence audit.Evidence
			if err := json.Unmarshal(response.Body.Bytes(), &evidence); err != nil {
				t.Fatal(err)
			}
			if evidence.EventCount != 0 || evidence.Status != "needs-review" || evidence.Verification == nil || evidence.Verification.Valid || evidence.Verification.Outcome != test.outcome {
				t.Fatalf("failed zero-row evidence disguised as empty: %#v", evidence)
			}
		})
	}
}
