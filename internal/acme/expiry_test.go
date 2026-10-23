package acme

import (
	"testing"
	"time"
)

func TestClassifyCertificateExpiryUsesInjectedClock(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	tests := map[string]struct {
		expiresAt time.Time
		want      CertificateExpiryStatus
	}{
		"ok":         {expiresAt: now.Add(RenewBefore + time.Second), want: CertificateExpiryOK},
		"renew soon": {expiresAt: now.Add(RenewBefore), want: CertificateExpiryRenewSoon},
		"expired":    {expiresAt: now, want: CertificateExpiryExpired},
	}

	for name, test := range tests {
		got := ClassifyCertificateExpiry(clock, test.expiresAt, 0)
		if got != test.want {
			t.Fatalf("%s: status = %q, want %q", name, got, test.want)
		}
	}
}

func TestClassifyCertificateExpiryUsesExplicitRenewWindow(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	got := ClassifyCertificateExpiry(clock, now.Add(2*time.Hour), time.Hour)
	if got != CertificateExpiryOK {
		t.Fatalf("status = %q, want %q", got, CertificateExpiryOK)
	}
}

func TestClassifyCertificateExpiryBandFourTier(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	tests := []struct {
		name   string
		expiry time.Time
		want   CertificateExpiryBand
	}{
		{"ok", now.Add(60 * 24 * time.Hour), CertificateExpiryBandOK},
		{"warn-30d at boundary", now.Add(30 * 24 * time.Hour), CertificateExpiryBandWarn30},
		{"warn-30d inside", now.Add(20 * 24 * time.Hour), CertificateExpiryBandWarn30},
		{"critical-7d at boundary", now.Add(7 * 24 * time.Hour), CertificateExpiryBandCritical7},
		{"critical-7d inside", now.Add(2 * 24 * time.Hour), CertificateExpiryBandCritical7},
		{"expired exact now", now, CertificateExpiryBandExpired},
		{"expired past", now.Add(-time.Hour), CertificateExpiryBandExpired},
	}
	for _, tc := range tests {
		got := ClassifyCertificateExpiryBand(clock, tc.expiry, 0, 0)
		if got.Band != tc.want {
			t.Fatalf("%s: band = %q, want %q (assessment %#v)", tc.name, got.Band, tc.want, got)
		}
		if tc.want != CertificateExpiryBandOK && got.Reason == "" {
			t.Fatalf("%s: expected reason for actionable band, got empty", tc.name)
		}
	}
}

func TestClassifyCertificateExpiryBandHonorsCustomWindows(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	got := ClassifyCertificateExpiryBand(clock, now.Add(2*time.Hour), 6*time.Hour, time.Hour)
	if got.Band != CertificateExpiryBandWarn30 {
		t.Fatalf("band = %q, want warn-30d (assessment %#v)", got.Band, got)
	}
	got = ClassifyCertificateExpiryBand(clock, now.Add(30*time.Minute), 6*time.Hour, time.Hour)
	if got.Band != CertificateExpiryBandCritical7 {
		t.Fatalf("band = %q, want critical-7d (assessment %#v)", got.Band, got)
	}
}

func TestClassifyCertificateExpiryBandClampsInvertedBounds(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	got := ClassifyCertificateExpiryBand(clock, now.Add(time.Hour), time.Hour, 24*time.Hour)
	if got.Band != CertificateExpiryBandCritical7 {
		t.Fatalf("inverted bounds should clamp critical to warn; band = %q", got.Band)
	}
}
