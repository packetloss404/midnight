package acme

import (
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
)

// Clock is the injected time source used by certificate expiry classifiers.
type Clock func() time.Time

// CertificateExpiryStatus is the renew-window classification used by the
// renewal scheduler. It exposes the legacy three-state taxonomy.
type CertificateExpiryStatus string

const (
	// CertificateExpiryOK marks a certificate that does not yet need attention.
	CertificateExpiryOK CertificateExpiryStatus = "ok"
	// CertificateExpiryRenewSoon marks a certificate inside the renew window.
	CertificateExpiryRenewSoon CertificateExpiryStatus = "renew-soon"
	// CertificateExpiryExpired marks a certificate past NotAfter.
	CertificateExpiryExpired CertificateExpiryStatus = "expired"
)

// CertificateExpiryBand is the four-tier operator-facing band derived from the
// distance between now and the certificate's NotAfter timestamp.
type CertificateExpiryBand string

const (
	// CertificateExpiryBandOK marks a certificate with more than WarnBefore left.
	CertificateExpiryBandOK CertificateExpiryBand = "ok"
	// CertificateExpiryBandWarn30 marks a certificate inside the 30-day warning window.
	CertificateExpiryBandWarn30 CertificateExpiryBand = "warn-30d"
	// CertificateExpiryBandCritical7 marks a certificate inside the 7-day critical window.
	CertificateExpiryBandCritical7 CertificateExpiryBand = "critical-7d"
	// CertificateExpiryBandExpired marks a certificate past NotAfter.
	CertificateExpiryBandExpired CertificateExpiryBand = "expired"
)

// CertificateExpiryWarn is the default boundary at which the warn-30d band starts.
const CertificateExpiryWarn = 30 * 24 * time.Hour

// CertificateExpiryCritical is the default boundary at which the critical-7d band starts.
const CertificateExpiryCritical = 7 * 24 * time.Hour

// CertificateExpiryAssessment is a structured expiry report produced by
// ClassifyCertificateExpiryBand. It carries the band, the offline timestamps
// used to compute it, the registered failure code (if any), and the operator
// recovery hint.
type CertificateExpiryAssessment struct {
	Band         CertificateExpiryBand `json:"band"`
	Now          time.Time             `json:"now"`
	ExpiresAt    time.Time             `json:"expiresAt"`
	TimeLeft     time.Duration         `json:"timeLeftSeconds"`
	Reason       failurecodes.Code     `json:"reason,omitempty"`
	RecoveryHint string                `json:"recoveryHint,omitempty"`
}

// ClassifyCertificateExpiry returns the renew-window classification using the
// existing three-state taxonomy. When renewBefore is zero, RenewBefore is used.
func ClassifyCertificateExpiry(now Clock, expiresAt time.Time, renewBefore time.Duration) CertificateExpiryStatus {
	if now == nil {
		now = time.Now
	}
	if renewBefore <= 0 {
		renewBefore = RenewBefore
	}

	current := now()
	if !expiresAt.After(current) {
		return CertificateExpiryExpired
	}
	if !expiresAt.After(current.Add(renewBefore)) {
		return CertificateExpiryRenewSoon
	}
	return CertificateExpiryOK
}

// ClassifyCertificateExpiryBand returns the four-tier operator band for an
// offline timestamp input. When warnBefore or criticalBefore are zero the
// package defaults (30 days, 7 days) are used. When clock is nil, time.Now is
// used. The returned assessment includes a registered failure code and the
// canonical recovery hint when the band signals action.
func ClassifyCertificateExpiryBand(clock Clock, expiresAt time.Time, warnBefore, criticalBefore time.Duration) CertificateExpiryAssessment {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	if warnBefore <= 0 {
		warnBefore = CertificateExpiryWarn
	}
	if criticalBefore <= 0 {
		criticalBefore = CertificateExpiryCritical
	}
	if criticalBefore > warnBefore {
		// Caller passed inverted bounds; clamp critical to the warn boundary.
		criticalBefore = warnBefore
	}

	now := clock()
	assessment := CertificateExpiryAssessment{
		Now:       now,
		ExpiresAt: expiresAt,
	}
	if !expiresAt.After(now) {
		assessment.Band = CertificateExpiryBandExpired
		assessment.Reason = failurecodes.ACMECertificateExpired
		assessment.RecoveryHint = failurecodes.RecoveryHint(failurecodes.ACMECertificateExpired)
		return assessment
	}
	timeLeft := expiresAt.Sub(now)
	assessment.TimeLeft = timeLeft
	switch {
	case timeLeft <= criticalBefore:
		assessment.Band = CertificateExpiryBandCritical7
		assessment.Reason = failurecodes.ACMECertificateExpiringCritical
		assessment.RecoveryHint = failurecodes.RecoveryHint(failurecodes.ACMECertificateExpiringCritical)
	case timeLeft <= warnBefore:
		assessment.Band = CertificateExpiryBandWarn30
		assessment.Reason = failurecodes.ACMECertificateExpiringWarn
		assessment.RecoveryHint = failurecodes.RecoveryHint(failurecodes.ACMECertificateExpiringWarn)
	default:
		assessment.Band = CertificateExpiryBandOK
	}
	return assessment
}
