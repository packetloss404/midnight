package backup

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

const (
	// BackupReadinessIssueDestinationSchemeUnsupported is emitted when the destination URI uses
	// a scheme that the operator did not allowlist.
	BackupReadinessIssueDestinationSchemeUnsupported = "destination-scheme-unsupported"
	// BackupReadinessIssueCredentialMissing is emitted when a required credential env var is unset
	// or whitespace-only.
	BackupReadinessIssueCredentialMissing = "destination-credential-missing"
	// BackupReadinessIssueCredentialMalformed is emitted when a required credential env var fails
	// the offline format check the operator declared.
	BackupReadinessIssueCredentialMalformed = "destination-credential-malformed"
	// BackupReadinessIssueKEKDEKCollision is emitted when the KEK ID equals the DEK ID — a
	// release-blocking invariant for envelope encryption.
	BackupReadinessIssueKEKDEKCollision = "kek-dek-collision"
	// BackupReadinessIssueKEKMetadataMissing is emitted when the KEK identifier is missing from
	// the readiness extension input.
	BackupReadinessIssueKEKMetadataMissing = "kek-metadata-missing"
	// BackupReadinessIssueFreeSpaceInsufficient is emitted when the declared free space is below
	// the manifest's declared total backup size.
	BackupReadinessIssueFreeSpaceInsufficient = "destination-free-space-insufficient"
	// BackupReadinessIssueFreeSpaceUndeclared is emitted when the operator did not declare a
	// free-space estimate alongside an off-host destination check.
	BackupReadinessIssueFreeSpaceUndeclared = "destination-free-space-undeclared"
)

// CredentialEnvSpec is a typed declaration that an environment variable must be present and
// pass an offline format check before a backup destination is treated as ready.
//
// Format is one of:
//   - "any"         — non-empty after trimming whitespace.
//   - "minlength:N" — non-empty and at least N runes after trimming whitespace.
//   - "prefix:S"    — non-empty and starts with literal prefix S.
type CredentialEnvSpec struct {
	Name   string
	Format string
}

// BackupReadinessExtension carries the additional Phase 1 readiness signals the extended review
// applies on top of the base review. All fields are optional; zero values mean "do not check".
type BackupReadinessExtension struct {
	// AllowedSchemes restricts the destination URI to a closed set (e.g. local, file, s3, gcs, sftp).
	// When empty, scheme validation is skipped (the base review still validates local/file via ParseLocalURI).
	AllowedSchemes []string
	// RequiredCredentialEnv lists env vars that must be present for the destination to operate.
	// Phase 1 only checks for presence and format — it never reads credential bytes off-host.
	RequiredCredentialEnv []CredentialEnvSpec
	// KEKID is the platform key encryption key identifier; required for the KEK separation invariant.
	KEKID string
	// DEKID is the per-backup data encryption key identifier; must differ from KEKID.
	DEKID string
	// FreeSpaceBytes is the operator-declared free-space estimate at the destination, in bytes.
	// When zero and EnforceFreeSpace is true, an undeclared-free-space warning is emitted.
	FreeSpaceBytes int64
	// EnforceFreeSpace turns on free-space checks. When false, FreeSpaceBytes is ignored.
	EnforceFreeSpace bool
	// LookupEnv is an optional override for os.LookupEnv used by tests; when nil, os.LookupEnv is used.
	LookupEnv func(string) (string, bool)
}

// ExtendedBackupReadinessSummary embeds the base readiness summary and records the additional
// extension findings.
type ExtendedBackupReadinessSummary struct {
	BackupReadinessSummary
	Extension ExtensionFindings `json:"extension"`
}

// ExtensionFindings records what the extension review concluded about scheme, credentials,
// envelope-key separation, and free space.
type ExtensionFindings struct {
	SchemeValid         bool   `json:"schemeValid"`
	Scheme              string `json:"scheme,omitempty"`
	CredentialChecks    int    `json:"credentialChecks"`
	CredentialIssues    int    `json:"credentialIssues"`
	KEKDEKSeparationOK  bool   `json:"kekDekSeparationOk"`
	FreeSpaceChecked    bool   `json:"freeSpaceChecked"`
	FreeSpaceBytes      int64  `json:"freeSpaceBytes"`
	DeclaredBackupBytes int64  `json:"declaredBackupBytes"`
	FreeSpaceSufficient bool   `json:"freeSpaceSufficient"`
}

// ReviewBackupReadinessExtended runs the base readiness review and layers Phase 1 additional checks.
//
// It is fully additive: callers that pass an empty extension get exactly the base review's behavior
// wrapped in the extended summary type.
func ReviewBackupReadinessExtended(input BackupReadinessInput, ext BackupReadinessExtension) ExtendedBackupReadinessSummary {
	base := ReviewBackupReadiness(input)
	summary := ExtendedBackupReadinessSummary{BackupReadinessSummary: base, Extension: ExtensionFindings{}}

	scheme := schemeOf(input.DestinationURI)
	summary.Extension.Scheme = scheme

	if len(ext.AllowedSchemes) > 0 {
		summary.Extension.SchemeValid = isAllowedScheme(scheme, ext.AllowedSchemes)
		if !summary.Extension.SchemeValid {
			summary.Issues = append(summary.Issues, blockedIssue(BackupReadinessIssueDestinationSchemeUnsupported, fmt.Sprintf("destination scheme %q is not in the operator allowlist", scheme)))
		}
	} else {
		summary.Extension.SchemeValid = base.DestinationValid
	}

	lookup := ext.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	for _, spec := range ext.RequiredCredentialEnv {
		if err := validateCredentialEnv(spec, lookup); err != nil {
			summary.Extension.CredentialIssues++
			if errors.Is(err, errCredentialMissing) {
				summary.Issues = append(summary.Issues, blockedIssue(BackupReadinessIssueCredentialMissing, fmt.Sprintf("credential env %q is not set", spec.Name)))
			} else {
				summary.Issues = append(summary.Issues, blockedIssue(BackupReadinessIssueCredentialMalformed, fmt.Sprintf("credential env %q failed format check %q: %v", spec.Name, spec.Format, err)))
			}
		}
	}
	summary.Extension.CredentialChecks = len(ext.RequiredCredentialEnv)

	if strings.TrimSpace(ext.KEKID) == "" || strings.TrimSpace(ext.DEKID) == "" {
		summary.Issues = append(summary.Issues, blockedIssue(BackupReadinessIssueKEKMetadataMissing, "KEK and DEK identifiers must be declared for envelope encryption"))
	} else if ext.KEKID == ext.DEKID {
		summary.Issues = append(summary.Issues, blockedIssue(BackupReadinessIssueKEKDEKCollision, "KEK identifier must differ from DEK identifier"))
	} else {
		summary.Extension.KEKDEKSeparationOK = true
	}

	if ext.EnforceFreeSpace {
		summary.Extension.FreeSpaceChecked = true
		summary.Extension.FreeSpaceBytes = ext.FreeSpaceBytes
		summary.Extension.DeclaredBackupBytes = input.Manifest.TotalBytes
		if ext.FreeSpaceBytes <= 0 {
			summary.Issues = append(summary.Issues, reviewIssue(BackupReadinessIssueFreeSpaceUndeclared, "free-space estimate must be declared with the off-host readiness check"))
		} else if input.Manifest.TotalBytes > ext.FreeSpaceBytes {
			summary.Issues = append(summary.Issues, blockedIssue(BackupReadinessIssueFreeSpaceInsufficient, fmt.Sprintf("declared backup size %d bytes exceeds free space %d bytes", input.Manifest.TotalBytes, ext.FreeSpaceBytes)))
		} else {
			summary.Extension.FreeSpaceSufficient = true
		}
	}

	summary.Status = readinessStatus(summary.Issues)
	summary.BackupReadinessSummary.Issues = summary.Issues
	summary.BackupReadinessSummary.Status = summary.Status
	return summary
}

func schemeOf(rawURI string) string {
	if strings.TrimSpace(rawURI) == "" {
		return ""
	}
	parsed, err := url.Parse(rawURI)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Scheme)
}

func isAllowedScheme(scheme string, allowed []string) bool {
	if scheme == "" {
		return false
	}
	for _, a := range allowed {
		if strings.EqualFold(scheme, a) {
			return true
		}
	}
	return false
}

var errCredentialMissing = errors.New("credential env not set")

func validateCredentialEnv(spec CredentialEnvSpec, lookup func(string) (string, bool)) error {
	if strings.TrimSpace(spec.Name) == "" {
		return errors.New("credential env name is required")
	}
	value, ok := lookup(spec.Name)
	value = strings.TrimSpace(value)
	if !ok || value == "" {
		return errCredentialMissing
	}
	format := strings.TrimSpace(spec.Format)
	if format == "" || format == "any" {
		return nil
	}
	switch {
	case strings.HasPrefix(format, "minlength:"):
		var min int
		if _, err := fmt.Sscanf(strings.TrimPrefix(format, "minlength:"), "%d", &min); err != nil {
			return fmt.Errorf("invalid minlength specifier: %w", err)
		}
		if len([]rune(value)) < min {
			return fmt.Errorf("credential value shorter than %d characters", min)
		}
		return nil
	case strings.HasPrefix(format, "prefix:"):
		want := strings.TrimPrefix(format, "prefix:")
		if !strings.HasPrefix(value, want) {
			return fmt.Errorf("credential value missing prefix %q", want)
		}
		return nil
	default:
		return fmt.Errorf("unknown credential format spec %q", format)
	}
}
