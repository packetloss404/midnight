package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-acme/lego/v4/challenge/dns01"
	"github.com/go-acme/lego/v4/registration"
	"github.com/midnight/midnight/internal/acme"
	"github.com/midnight/midnight/internal/dns"
)

// This lane exercises the native issuer and production DNS HTTP adapter. It
// deliberately does not claim the installed domain worker or ingress path.
func (r *runner) dnsPreflight(ctx context.Context) error {
	if _, _, err := stagingAccount(r.cfg); err != nil {
		return err
	}
	token, err := r.cfg.secret("CLOUDFLARE_TOKEN_FILE")
	if err != nil {
		return err
	}
	var zone struct {
		Success bool `json:"success"`
		Result  struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"result"`
	}
	_, err = r.request(ctx, "GET", "https://api.cloudflare.com/client/v4/zones/"+r.cfg.values["DNS_ZONE_ID"], map[string]string{"Authorization": "Bearer " + token}, nil, &zone)
	if err != nil {
		return err
	}
	if !zone.Success || zone.Result.ID != r.cfg.values["DNS_ZONE_ID"] || zone.Result.Name != r.cfg.values["DNS_ZONE_NAME"] || zone.Result.Status != "active" {
		return blocked("DNS_ZONE_IDENTITY_OR_AUTHORITY_MISMATCH")
	}
	r.receipt.pass("EXISTING_STAGING_ACCOUNT_AND_EXACT_ACTIVE_DNS_ZONE")
	r.receipt.incomplete("INSTALLED_DOMAIN_WORKER_INGRESS_PRODUCTION_CERTIFICATE_AND_RENEWAL")
	return nil
}

func stagingAccount(c config) ([]byte, []byte, error) {
	dir := c.path("ACME_STAGING_ACCOUNT_DIR")
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, nil, blocked("EXISTING_PRIVATE_STAGING_ACCOUNT_REQUIRED")
	}
	key, err := readPrivate(filepath.Join(dir, "account.key"), 64<<10)
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode(key)
	if block == nil || block.Type != "EC PRIVATE KEY" {
		return nil, nil, blocked("STAGING_ACCOUNT_KEY_INVALID")
	}
	if _, err := x509.ParseECPrivateKey(block.Bytes); err != nil {
		return nil, nil, blocked("STAGING_ACCOUNT_KEY_INVALID")
	}
	reg, err := readPrivate(filepath.Join(dir, "registration.json"), 64<<10)
	if err != nil {
		return nil, nil, err
	}
	var account registration.Resource
	if err := decodeStrict(reg, &account); err != nil {
		return nil, nil, blocked("STAGING_ACCOUNT_REGISTRATION_INVALID")
	}
	u, err := url.Parse(account.URI)
	if err != nil || u.Scheme != "https" || u.Host != "acme-staging-v02.api.letsencrypt.org" || !strings.HasPrefix(u.Path, "/acme/acct/") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || account.Body.Status != "valid" {
		return nil, nil, blocked("EXISTING_STAGING_ACCOUNT_REQUIRED")
	}
	return key, reg, nil
}

type dnsState struct {
	Domain string    `json:"domain"`
	Until  time.Time `json:"until"`
}
type dnsIntent struct {
	Name            string `json:"name"`
	Digest          string `json:"content_sha256"`
	ID              string `json:"provider_record_id,omitempty"`
	DeleteConfirmed bool   `json:"delete_confirmed,omitempty"`
}
type dnsProof struct {
	Issued            bool   `json:"issued"`
	ProviderReadback  bool   `json:"provider_readback"`
	CertificateSHA256 string `json:"certificate_sha256"`
}

func (r *runner) dnsProvider() (dns.Provider, error) {
	token, err := r.cfg.secret("CLOUDFLARE_TOKEN_FILE")
	if err != nil {
		return nil, err
	}
	p, err := dns.NewCloudflareProvider(dns.CloudflareConfig{Token: token, HTTPClient: r.client, MaxAttempts: 1})
	if err != nil {
		return nil, blocked("DNS_PROVIDER_CONFIGURATION_INVALID")
	}
	return p, nil
}

func (r *runner) dnsACME(ctx context.Context, envFile string) (result error) {
	key, registration, err := stagingAccount(r.cfg)
	if err != nil {
		return err
	}
	defer clear(key)
	defer clear(registration)
	provider, err := r.dnsProvider()
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "midnight-live-acceptance-"+r.receipt.RunID+"-")
	if err != nil {
		return failed("PRIVATE_SCRATCH_UNAVAILABLE")
	}
	// Remove only the directory returned by MkdirTemp, never an operator path.
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		cleanupErr := r.finishDNSCleanup(cleanupCtx, provider, "_acme-challenge.qa-"+r.receipt.RunID+"."+r.cfg.values["DNS_QA_SUFFIX"], dir)
		result = cleanupError(result, cleanupErr)
	}()
	until, _ := ctx.Deadline()
	state := dnsState{Domain: "qa-" + r.receipt.RunID + "." + r.cfg.values["DNS_QA_SUFFIX"], Until: until}
	if len(state.Domain) > 240 {
		return blocked("DISPOSABLE_DNS_NAME_TOO_LONG")
	}
	for name, data := range map[string][]byte{"account.key": key, "registration.json": registration} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			return failed("PRIVATE_SCRATCH_WRITE_FAILED")
		}
	}
	if err := writePrivateJSON(filepath.Join(dir, "input.json"), state); err != nil {
		return err
	}
	if err := writePrivateJSON(filepath.Join(dir, "journal.json"), []dnsIntent{}); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return failed("CHILD_EXECUTABLE_UNAVAILABLE")
	}
	// The child consumes the validated parent configuration, never reopens
	// the operator's original file after preflight. Resolve relative paths
	// before moving this immutable snapshot into its private run directory.
	snapshot := snapshotConfiguration(r.cfg)
	snapshotPath := filepath.Join(dir, "configuration.env")
	if err := os.WriteFile(snapshotPath, snapshot, 0600); err != nil {
		return failed("PRIVATE_CONFIGURATION_SNAPSHOT_FAILED")
	}
	cmd := exec.CommandContext(ctx, executable, "--lane", "dns-acme", "--env-file", snapshotPath, "--execute", "--internal-dns-state", dir)
	// Lego does not propagate the caller context. A child gives the complete
	// DNS propagation/order operation a deadline and suppresses vendor logs.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return failed("DNS_ACME_DEADLINE_EXCEEDED")
		}
		return failed("NATIVE_STAGING_ISSUANCE_FAILED")
	}
	data, err := readPrivate(filepath.Join(dir, "proof.json"), 4096)
	if err != nil {
		return failed("STAGING_PROOF_MISSING")
	}
	var proof dnsProof
	if decodeStrict(data, &proof) != nil || !proof.Issued || !proof.ProviderReadback || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(proof.CertificateSHA256) {
		return failed("STAGING_PROOF_INVALID")
	}
	r.receipt.pass("NATIVE_ISSUER_REAL_DNS01_STAGING_CERTIFICATE_AND_SAN")
	r.receipt.pass("PRODUCTION_DNS_ADAPTER_CREATE_READBACK_AND_OWNED_DELETE")
	return nil
}

func dnsChild(c config, dir string) error {
	if err := c.authorize("dns-acme"); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return blocked("PRIVATE_CHILD_STATE_REQUIRED")
	}
	data, err := readPrivate(filepath.Join(dir, "input.json"), 4096)
	if err != nil {
		return err
	}
	var state dnsState
	if decodeStrict(data, &state) != nil || !regexp.MustCompile(`^qa-[a-f0-9]{24}\.`+regexp.QuoteMeta(c.values["DNS_QA_SUFFIX"])+`$`).MatchString(state.Domain) || time.Until(state.Until) <= 0 || time.Until(state.Until) > c.deadline {
		return failed("DNS_CHILD_SCOPE_INVALID")
	}
	ctx, cancel := context.WithDeadline(context.Background(), state.Until)
	defer cancel()
	r := newRunner(c, &receipt{})
	if err := r.dnsPreflight(ctx); err != nil {
		return err
	}
	provider, err := r.dnsProvider()
	if err != nil {
		return err
	}
	challenge := &scopedChallenge{ctx: ctx, provider: provider, zone: c.values["DNS_ZONE_ID"], domain: state.Domain, stateDir: dir}
	// Validate the copied account too. Without both original and copy validation
	// Lego's load-or-register fallback could silently create a new account.
	copyConfig := c
	copyConfig.values = map[string]string{"ACME_STAGING_ACCOUNT_DIR": dir}
	if _, _, err := stagingAccount(copyConfig); err != nil {
		return err
	}
	issuer, err := acme.NewLegoIssuer(acme.LegoIssuerConfig{Environment: acme.LegoEnvironmentStaging, Email: c.values["ACME_EMAIL"], StateDir: dir, Provider: challenge, ProviderID: "cloudflare", HTTPClientTimeout: 15 * time.Second})
	if err != nil {
		return failed("NATIVE_STAGING_ISSUER_UNAVAILABLE")
	}
	issued, err := issuer.Issue(ctx, acme.IssueRequest{Domains: []string{state.Domain}, BundleCA: true})
	if err != nil {
		return failed("NATIVE_STAGING_ISSUANCE_FAILED")
	}
	certPEM, err := os.ReadFile(issued.CertPath)
	if err != nil {
		return failed("STAGING_CERTIFICATE_MISSING")
	}
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return failed("STAGING_CERTIFICATE_INVALID")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || len(cert.DNSNames) != 1 || cert.DNSNames[0] != state.Domain || cert.VerifyHostname(state.Domain) != nil || time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) {
		return failed("STAGING_CERTIFICATE_SAN_OR_VALIDITY_MISMATCH")
	}
	u, err := url.Parse(issued.IssuerURL)
	if err != nil || u.Scheme != "https" || u.Host != "acme-staging-v02.api.letsencrypt.org" {
		return failed("STAGING_CERTIFICATE_AUTHORITY_MISMATCH")
	}
	sum := sha256.Sum256(cert.Raw)
	return writePrivateJSON(filepath.Join(dir, "proof.json"), dnsProof{Issued: true, ProviderReadback: challenge.readback, CertificateSHA256: hex.EncodeToString(sum[:])})
}

type scopedChallenge struct {
	ctx                    context.Context
	provider               dns.Provider
	zone, domain, stateDir string
	readback               bool
}

func (p *scopedChallenge) Present(domain, token, keyAuth string) error {
	info := dns01.GetChallengeInfo(domain, keyAuth)
	name := "_acme-challenge." + p.domain
	if domain != p.domain || strings.TrimSuffix(info.FQDN, ".") != name || strings.TrimSuffix(info.EffectiveFQDN, ".") != name {
		return failed("DNS_CHALLENGE_OUTSIDE_PINNED_SCOPE")
	}
	rows, err := p.provider.ListRecords(p.ctx, p.zone)
	if err != nil {
		return failed("DNS_PROVIDER_READ_FAILED")
	}
	for _, record := range rows {
		if strings.TrimSuffix(record.Name, ".") == name {
			return failed("DNS_CHALLENGE_NAME_NOT_EMPTY")
		}
	}
	intent := dnsIntent{Name: name, Digest: digest(info.Value)}
	// Journal before the write so a killed process or lost response can still
	// clean only this run's exact TXT value, without retaining the value itself.
	if err := writePrivateJSON(filepath.Join(p.stateDir, "journal.json"), []dnsIntent{intent}); err != nil {
		return err
	}
	record, err := p.provider.CreateRecord(p.ctx, p.zone, dns.DesiredRecord{Type: dns.TypeTXT, Name: name, Content: info.Value, TTL: 60})
	if err != nil {
		return failed("DNS_CHALLENGE_CREATE_FAILED")
	}
	intent.ID = record.ID
	if record.Type != dns.TypeTXT || record.Name != name || digest(record.Content) != intent.Digest || record.ZoneID != p.zone || record.ID == "" {
		return failed("DNS_CHALLENGE_RESPONSE_MISMATCH")
	}
	if err := writePrivateJSON(filepath.Join(p.stateDir, "journal.json"), []dnsIntent{intent}); err != nil {
		return err
	}
	rows, err = p.provider.ListRecords(p.ctx, p.zone)
	if err != nil {
		return failed("DNS_CHALLENGE_READBACK_FAILED")
	}
	for _, item := range rows {
		if item.ID == record.ID && item.Name == name && item.Type == dns.TypeTXT && digest(item.Content) == intent.Digest {
			p.readback = true
			return nil
		}
	}
	return failed("DNS_CHALLENGE_READBACK_MISSING")
}
func (p *scopedChallenge) CleanUp(domain, token, keyAuth string) error {
	if domain != p.domain {
		return failed("DNS_CHALLENGE_OUTSIDE_PINNED_SCOPE")
	}
	return cleanupDNS(p.ctx, p.provider, p.zone, "_acme-challenge."+p.domain, p.stateDir)
}
func cleanupDNS(ctx context.Context, provider dns.Provider, zone, name, dir string) error {
	return cleanupDNSWithInterval(ctx, provider, zone, name, dir, time.Second)
}
func cleanupDNSWithInterval(ctx context.Context, provider dns.Provider, zone, name, dir string, interval time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	data, err := readPrivate(filepath.Join(dir, "journal.json"), 4096)
	// No journal means no writes were permitted. Still check the exact fresh
	// name below, so ambiguous state cannot be labelled cleaned up.
	var intents []dnsIntent
	if err == nil {
		if decodeStrict(data, &intents) != nil || len(intents) > 1 {
			return failed("DNS_OWNERSHIP_JOURNAL_INVALID")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		if _, statErr := os.Lstat(filepath.Join(dir, "journal.json")); !errors.Is(statErr, os.ErrNotExist) {
			return failed("DNS_OWNERSHIP_JOURNAL_UNAVAILABLE")
		}
	}
	for _, intent := range intents {
		if intent.Name != name || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(intent.Digest) || intent.DeleteConfirmed && intent.ID == "" {
			return failed("DNS_OWNERSHIP_JOURNAL_INVALID")
		}
	}
	for {
		rows, err := provider.ListRecords(ctx, zone)
		if err != nil {
			return failed("DNS_CLEANUP_READ_FAILED")
		}
		for _, record := range rows {
			if strings.TrimSuffix(record.Name, ".") != name {
				continue
			}
			if len(intents) != 1 || record.Type != dns.TypeTXT || digest(record.Content) != intents[0].Digest || (intents[0].ID != "" && intents[0].ID != record.ID) {
				return failed("DNS_CLEANUP_OWNERSHIP_CHANGED")
			}
			intents[0].ID = record.ID
			if err := writePrivateJSON(filepath.Join(dir, "journal.json"), intents); err != nil {
				return err
			}
			if err := provider.DeleteRecord(ctx, zone, record.ID); err != nil {
				return failed("DNS_CLEANUP_DELETE_FAILED")
			}
			intents[0].DeleteConfirmed = true
			if err := writePrivateJSON(filepath.Join(dir, "journal.json"), intents); err != nil {
				return err
			}
		}
		if len(intents) == 0 || intents[0].DeleteConfirmed {
			break
		}
		// Even repeated absence cannot prove an in-flight create failed.
		// Recover only after observing its exact value and deleting its ID;
		// otherwise preserve the uncertainty and ownership journal.
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return failed("DNS_WRITE_OUTCOME_UNKNOWN")
		case <-timer.C:
		}
	}
	rows, err := provider.ListRecords(ctx, zone)
	if err != nil {
		return failed("DNS_CLEANUP_READ_FAILED")
	}
	for _, record := range rows {
		if strings.TrimSuffix(record.Name, ".") == name {
			return failed("DNS_CLEANUP_RECORD_REMAINS")
		}
	}
	return nil
}

func (r *runner) finishDNSCleanup(ctx context.Context, provider dns.Provider, name, dir string) error {
	r.receipt.Cleanup.Attempted = true
	cleanupErr := cleanupDNS(ctx, provider, r.cfg.values["DNS_ZONE_ID"], name, dir)
	preserved := false
	if cleanupErr != nil {
		journalID := "acceptance-dns-" + r.receipt.RunID + ".json"
		r.receipt.Cleanup.ResidualJournalID = journalID
		data, err := readPrivate(filepath.Join(dir, "journal.json"), 4096)
		var intents []dnsIntent
		valid := err == nil && decodeStrict(data, &intents) == nil && len(intents) <= 1
		if !valid {
			intents = nil
		}
		snapshot := struct {
			Schema  string      `json:"schema_version"`
			RunID   string      `json:"run_id"`
			Zone    string      `json:"zone_id"`
			Name    string      `json:"record_name"`
			Valid   bool        `json:"journal_valid"`
			Intents []dnsIntent `json:"intents"`
		}{"midnight.dns-residual/v1", r.receipt.RunID, r.cfg.values["DNS_ZONE_ID"], name, valid, intents}
		if err := writeExclusivePrivateJSON(filepath.Join(r.cfg.directory, journalID), snapshot); err == nil {
			preserved = true
			r.receipt.Cleanup.ResidualJournalLocation = "CONFIG_DIRECTORY"
		}
	}
	if cleanupErr == nil || preserved {
		if err := os.RemoveAll(dir); err != nil {
			cleanupErr = failed("PRIVATE_SCRATCH_CLEANUP_FAILED")
		}
		if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
			r.receipt.Cleanup.PrivateStateRemoved = true
		} else {
			cleanupErr = failed("PRIVATE_SCRATCH_CLEANUP_UNVERIFIED")
		}
	} else {
		// If the configured destination is unavailable, keep only the
		// sanitized journal in the private run-named temporary directory.
		entries, err := os.ReadDir(dir)
		privateRemoved := err == nil
		for _, entry := range entries {
			if entry.Name() == "journal.json" {
				continue
			}
			if os.RemoveAll(filepath.Join(dir, entry.Name())) != nil {
				privateRemoved = false
			}
		}
		r.receipt.Cleanup.PrivateStateRemoved = privateRemoved
		r.receipt.Cleanup.ResidualJournalLocation = "PRIVATE_TEMP_DIRECTORY"
	}
	r.receipt.Cleanup.Verified = cleanupErr == nil
	return cleanupErr
}
func writeExclusivePrivateJSON(path string, value any) error {
	if err := requirePrivateParent(path); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return failed("RESIDUAL_JOURNAL_ENCODING_FAILED")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return failed("RESIDUAL_JOURNAL_PRESERVATION_FAILED")
	}
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return failed("RESIDUAL_JOURNAL_PRESERVATION_FAILED")
	}
	return nil
}

func snapshotConfiguration(c config) []byte {
	paths := map[string]bool{"QA_HUMAN_TOKEN_FILE": true, "GITHUB_PRIVATE_KEY_FILE": true, "GITHUB_QA_DRIVER_TOKEN_FILE": true, "CLOUDFLARE_TOKEN_FILE": true, "ACME_STAGING_ACCOUNT_DIR": true, "QA_RUNTIME_SSH_KEY_FILE": true, "QA_RUNTIME_SSH_KNOWN_HOSTS_FILE": true}
	keys := make([]string, 0, len(c.values))
	for key := range c.values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out strings.Builder
	for _, key := range keys {
		value := c.values[key]
		if paths[key] && value != "" {
			value = c.path(key)
		}
		out.WriteString(key + "=" + value + "\n")
	}
	return []byte(out.String())
}
func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
func writePrivateJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return failed("PRIVATE_STATE_ENCODING_FAILED")
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".acceptance-state-*")
	if err != nil {
		return failed("PRIVATE_STATE_WRITE_FAILED")
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err = temp.Chmod(0600); err == nil {
		_, err = temp.Write(data)
	}
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tempPath, path)
	}
	if err != nil {
		return failed("PRIVATE_STATE_WRITE_FAILED")
	}
	return nil
}
