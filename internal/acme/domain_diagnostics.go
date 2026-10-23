package acme

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"
)

type DomainOwnershipStatus string
type DomainRoutingStatus string
type DomainChallengeStatus string
type DomainCAAStatus string

const (
	DomainOwnershipUnknown  DomainOwnershipStatus = "unknown"
	DomainOwnershipVerified DomainOwnershipStatus = "verified"
	DomainOwnershipPending  DomainOwnershipStatus = "pending"
	DomainOwnershipFailed   DomainOwnershipStatus = "failed"

	DomainRoutingUnknown DomainRoutingStatus = "unknown"
	DomainRoutingReady   DomainRoutingStatus = "ready"
	DomainRoutingPending DomainRoutingStatus = "pending"
	DomainRoutingFailed  DomainRoutingStatus = "failed"

	DomainChallengeUnknown DomainChallengeStatus = "unknown"
	DomainChallengePending DomainChallengeStatus = "pending"
	DomainChallengeReady   DomainChallengeStatus = "ready"
	DomainChallengeFailed  DomainChallengeStatus = "failed"

	DomainCAAUnknown DomainCAAStatus = "unknown"
	DomainCAAAllowed DomainCAAStatus = "allowed"
	DomainCAABlocked DomainCAAStatus = "blocked"
)

// DomainDiagnostics is a network-free snapshot of certificate readiness inputs.
// It records observations supplied by callers; it never performs DNS or ACME IO.
type DomainDiagnostics struct {
	Domain               string                `json:"domain,omitempty"`
	Ownership            DomainOwnershipStatus `json:"ownership"`
	Routing              DomainRoutingStatus   `json:"routing"`
	Challenge            DomainChallengeStatus `json:"challenge"`
	CAA                  DomainCAAStatus       `json:"caa"`
	LastError            string                `json:"lastError,omitempty"`
	CertificateExpiresAt time.Time             `json:"certificateExpiresAt,omitempty"`
}

// DomainDiagnosticsSupportView omits domain names and error text so diagnostics
// can be included in support bundles without leaking tenant-controlled values.
type DomainDiagnosticsSupportView struct {
	DomainHash           string                `json:"domainHash,omitempty"`
	Ownership            DomainOwnershipStatus `json:"ownership"`
	Routing              DomainRoutingStatus   `json:"routing"`
	Challenge            DomainChallengeStatus `json:"challenge"`
	CAA                  DomainCAAStatus       `json:"caa"`
	HasLastError         bool                  `json:"hasLastError"`
	CertificateExpiresAt time.Time             `json:"certificateExpiresAt,omitempty"`
}

type CAARecord struct {
	Tag      string `json:"tag"`
	Value    string `json:"value"`
	Critical bool   `json:"critical,omitempty"`
}

// CAAPreflightResult records caller-supplied CAA observations. It is a data
// model only; construction and storage never perform DNS lookups.
type CAAPreflightResult struct {
	Domain    string          `json:"domain,omitempty"`
	Status    DomainCAAStatus `json:"status"`
	Records   []CAARecord     `json:"records,omitempty"`
	CheckedAt time.Time       `json:"checkedAt,omitempty"`
	LastError string          `json:"lastError,omitempty"`
}

type DomainDiagnosticsStore struct {
	mu    sync.RWMutex
	items map[string]DomainDiagnostics
}

func NewDomainDiagnosticsStore() *DomainDiagnosticsStore {
	return &DomainDiagnosticsStore{items: make(map[string]DomainDiagnostics)}
}

func (store *DomainDiagnosticsStore) Upsert(diagnostics DomainDiagnostics) DomainDiagnostics {
	if store == nil {
		return normalizeDomainDiagnostics(diagnostics)
	}

	diagnostics = normalizeDomainDiagnostics(diagnostics)
	if diagnostics.Domain == "" {
		return diagnostics
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.items == nil {
		store.items = make(map[string]DomainDiagnostics)
	}
	store.items[diagnostics.Domain] = diagnostics
	return diagnostics
}

func (store *DomainDiagnosticsStore) Get(domain string) (DomainDiagnostics, bool) {
	if store == nil {
		return DomainDiagnostics{}, false
	}

	store.mu.RLock()
	defer store.mu.RUnlock()
	diagnostics, ok := store.items[normalizeDomain(domain)]
	return diagnostics, ok
}

func (store *DomainDiagnosticsStore) GetSupportView(domain string) (DomainDiagnosticsSupportView, bool) {
	diagnostics, ok := store.Get(domain)
	if !ok {
		return DomainDiagnosticsSupportView{}, false
	}
	return diagnostics.SupportView(), true
}

func (store *DomainDiagnosticsStore) ListSupportViews() []DomainDiagnosticsSupportView {
	if store == nil {
		return nil
	}

	store.mu.RLock()
	defer store.mu.RUnlock()
	domains := make([]string, 0, len(store.items))
	for domain := range store.items {
		domains = append(domains, domain)
	}
	sort.Strings(domains)

	views := make([]DomainDiagnosticsSupportView, 0, len(domains))
	for _, domain := range domains {
		views = append(views, store.items[domain].SupportView())
	}
	return views
}

func (diagnostics DomainDiagnostics) SupportView() DomainDiagnosticsSupportView {
	view := DomainDiagnosticsSupportView{
		DomainHash:           domainHash(diagnostics.Domain),
		Ownership:            normalizeOwnershipStatus(diagnostics.Ownership),
		Routing:              normalizeRoutingStatus(diagnostics.Routing),
		Challenge:            normalizeChallengeStatus(diagnostics.Challenge),
		CAA:                  normalizeCAAStatus(diagnostics.CAA),
		HasLastError:         strings.TrimSpace(diagnostics.LastError) != "",
		CertificateExpiresAt: diagnostics.CertificateExpiresAt,
	}
	return view
}

func (result CAAPreflightResult) Normalized() CAAPreflightResult {
	result.Domain = normalizeDomain(result.Domain)
	result.Status = normalizeCAAStatus(result.Status)
	result.LastError = strings.TrimSpace(result.LastError)
	return result
}

func normalizeDomainDiagnostics(diagnostics DomainDiagnostics) DomainDiagnostics {
	diagnostics.Domain = normalizeDomain(diagnostics.Domain)
	diagnostics.Ownership = normalizeOwnershipStatus(diagnostics.Ownership)
	diagnostics.Routing = normalizeRoutingStatus(diagnostics.Routing)
	diagnostics.Challenge = normalizeChallengeStatus(diagnostics.Challenge)
	diagnostics.CAA = normalizeCAAStatus(diagnostics.CAA)
	diagnostics.LastError = strings.TrimSpace(diagnostics.LastError)
	return diagnostics
}

func normalizeDomain(domain string) string {
	return strings.ToLower(strings.TrimSpace(domain))
}

func domainHash(domain string) string {
	domain = normalizeDomain(domain)
	if domain == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(domain))
	return hex.EncodeToString(sum[:])
}

func normalizeOwnershipStatus(status DomainOwnershipStatus) DomainOwnershipStatus {
	if status == "" {
		return DomainOwnershipUnknown
	}
	return status
}

func normalizeRoutingStatus(status DomainRoutingStatus) DomainRoutingStatus {
	if status == "" {
		return DomainRoutingUnknown
	}
	return status
}

func normalizeChallengeStatus(status DomainChallengeStatus) DomainChallengeStatus {
	if status == "" {
		return DomainChallengeUnknown
	}
	return status
}

func normalizeCAAStatus(status DomainCAAStatus) DomainCAAStatus {
	if status == "" {
		return DomainCAAUnknown
	}
	return status
}
