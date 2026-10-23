package acme

import (
	"errors"
	"strings"
)

const manualDNSTTLSeconds = 300

// ManualDNSInstruction is a human-executable DNS-01 update with no DNS IO.
type ManualDNSInstruction struct {
	RecordName string `json:"recordName"`
	RecordType string `json:"recordType"`
	Value      string `json:"value"`
	TTLSeconds int    `json:"ttlSeconds"`
	Action     string `json:"action"`
}

// GenerateManualDNSInstruction returns the TXT record instruction needed to
// satisfy an ACME DNS-01 challenge manually.
func GenerateManualDNSInstruction(fqdn, value string) (ManualDNSInstruction, error) {
	fqdn = strings.TrimSpace(fqdn)
	value = strings.TrimSpace(value)
	if fqdn == "" {
		return ManualDNSInstruction{}, errors.New("manual DNS record name is required")
	}
	if value == "" {
		return ManualDNSInstruction{}, errors.New("manual DNS record value is required")
	}

	return ManualDNSInstruction{
		RecordName: fqdn,
		RecordType: "TXT",
		Value:      value,
		TTLSeconds: manualDNSTTLSeconds,
		Action:     "create-or-update",
	}, nil
}
