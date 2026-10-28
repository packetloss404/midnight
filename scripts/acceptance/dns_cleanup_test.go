package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/dns"
)

type delayedDNSDouble struct {
	dnsDouble
	reads        int
	appearOnRead int
	pending      *dns.Record
}

func (d *delayedDNSDouble) ListRecords(ctx context.Context, zone string) ([]dns.Record, error) {
	d.reads++
	if d.pending != nil && d.appearOnRead > 0 && d.reads >= d.appearOnRead {
		d.records = append(d.records, *d.pending)
		d.pending = nil
	}
	return d.dnsDouble.ListRecords(ctx, zone)
}

func TestDNSCleanupWaitsForDelayedCreateThenDeletesObservedID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("private journal permissions are enforced on Linux")
	}
	dir := privateTempDir(t)
	name := "_acme-challenge.qa.example.invalid"
	if err := writePrivateJSON(filepath.Join(dir, "journal.json"), []dnsIntent{{Name: name, Digest: digest("challenge")}}); err != nil {
		t.Fatal(err)
	}
	provider := &delayedDNSDouble{appearOnRead: 3, pending: &dns.Record{ID: "late_record", ZoneID: "zone_qa", Type: dns.TypeTXT, Name: name, Content: "challenge"}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := cleanupDNSWithInterval(ctx, provider, "zone_qa", name, dir, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if provider.reads < 4 || provider.deletes != 1 || provider.pending != nil || len(provider.records) != 0 {
		t.Fatal("early absence accepted before delayed create committed")
	}
	data, err := readPrivate(filepath.Join(dir, "journal.json"), 4096)
	if err != nil {
		t.Fatal(err)
	}
	var intents []dnsIntent
	if decodeStrict(data, &intents) != nil || len(intents) != 1 || intents[0].ID != "late_record" || !intents[0].DeleteConfirmed {
		t.Fatal("observed ID/deletion acknowledgement not persisted")
	}
}

func TestDNSUnobservedCreateNeverBecomesVerifiedAbsence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("private journal permissions are enforced on Linux")
	}
	for _, id := range []string{"", "known_but_not_deleted"} {
		dir := privateTempDir(t)
		name := "_acme-challenge.qa.example.invalid"
		if err := writePrivateJSON(filepath.Join(dir, "journal.json"), []dnsIntent{{Name: name, Digest: digest("challenge"), ID: id}}); err != nil {
			t.Fatal(err)
		}
		provider := &delayedDNSDouble{pending: &dns.Record{ID: "late_record", Type: dns.TypeTXT, Name: name, Content: "challenge"}}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		err := cleanupDNSWithInterval(ctx, provider, "zone_qa", name, dir, time.Millisecond)
		cancel()
		if err == nil || provider.deletes != 0 {
			t.Fatal("absence while create outcome unknown was reported cleaned")
		}
		// The write can become visible after the bounded cleanup window.
		// Its receipt must already be unverified, rather than green.
		provider.appearOnRead = provider.reads + 1
		rows, _ := provider.ListRecords(context.Background(), "zone_qa")
		if len(rows) != 1 {
			t.Fatal("late-commit fixture did not commit")
		}
	}
}

func TestDNSFailurePreservesPrivateJournalAndErasesCopiedSecrets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("private journal permissions are enforced on Linux")
	}
	dir := privateTempDir(t)
	configDir := privateTempDir(t)
	name := "_acme-challenge.qa.example.invalid"
	runID := strings.Repeat("a", 24)
	if err := writePrivateJSON(filepath.Join(dir, "journal.json"), []dnsIntent{{Name: name, Digest: digest("challenge")}}); err != nil {
		t.Fatal(err)
	}
	for file, secret := range map[string]string{"account.key": "account-private-material", "registration.json": "private-registration", "input.json": "private-child-state", "proof.json": "private-proof"} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(secret), 0600); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Mkdir(filepath.Join(dir, "certs"), 0700)
	_ = os.WriteFile(filepath.Join(dir, "certs", "privkey.pem"), []byte("certificate-private-material"), 0600)
	r := newRunner(config{directory: configDir, values: map[string]string{"DNS_ZONE_ID": "zone_qa"}}, &receipt{RunID: runID})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	err := r.finishDNSCleanup(ctx, &dnsDouble{}, name, dir)
	if err == nil || r.receipt.Cleanup.Verified || !r.receipt.Cleanup.PrivateStateRemoved || r.receipt.Cleanup.ResidualJournalLocation != "CONFIG_DIRECTORY" {
		t.Fatal("uncertain cleanup lost its failure or private-state status")
	}
	journal := filepath.Join(configDir, "acceptance-dns-"+runID+".json")
	data, err := readPrivate(journal, 8192)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"account-private-material", "private-registration", "private-child-state", "private-proof", "certificate-private-material", "\"challenge\""} {
		if strings.Contains(string(data), secret) {
			t.Fatal("residual journal retained secret state")
		}
	}
	if !strings.Contains(string(data), digest("challenge")) || !strings.Contains(string(data), name) {
		t.Fatal("ownership proof not retained")
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatal("child private scratch still exists")
	}
}

func TestDNSUnsafeJournalDestinationRetainsOnlySanitizedPrivateSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix private journal boundary")
	}
	dir := privateTempDir(t)
	destination := privateTempDir(t)
	_ = os.Chmod(destination, 0777)
	name := "_acme-challenge.qa.example.invalid"
	if err := writePrivateJSON(filepath.Join(dir, "journal.json"), []dnsIntent{{Name: name, Digest: digest("challenge")}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "account.key"), []byte("must-be-erased"), 0600); err != nil {
		t.Fatal(err)
	}
	r := newRunner(config{directory: destination, values: map[string]string{"DNS_ZONE_ID": "zone_qa"}}, &receipt{RunID: strings.Repeat("b", 24)})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if err := r.finishDNSCleanup(ctx, &dnsDouble{}, name, dir); err == nil || r.receipt.Cleanup.Verified || !r.receipt.Cleanup.PrivateStateRemoved || r.receipt.Cleanup.ResidualJournalLocation != "PRIVATE_TEMP_DIRECTORY" {
		t.Fatal("unsafe journal destination was trusted or private keys retained")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "journal.json" {
		t.Fatal("fallback must retain only ownership journal")
	}
	if _, err := readPrivate(filepath.Join(dir, "journal.json"), 4096); err != nil {
		t.Fatal(err)
	}
}
