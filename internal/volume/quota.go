package volume

import (
	"strings"

	"github.com/midnight/midnight/internal/failurecodes"
)

// QuotaSupport classifies whether the host filesystem can enforce
// per-project quotas at create time. The runner consults this before
// returning the typed VOLUME_QUOTA_UNSUPPORTED failure.
type QuotaSupport struct {
	FSType            string
	ProjectQuotaReady bool
	Message           string
}

// EvaluateQuotaSupport returns ok=true when the host filesystem
// reports a configuration that supports prjquota (xfs with
// `prjquota` mount option, ext4 with `quota` mount option).
//
// The function is purely classification — Lane 2's host probe runs
// the actual `findmnt` / `tune2fs` commands and feeds the parsed
// result through here.
func EvaluateQuotaSupport(fsType, mountOptions string) QuotaSupport {
	fs := strings.ToLower(strings.TrimSpace(fsType))
	opts := strings.ToLower(strings.TrimSpace(mountOptions))
	switch fs {
	case "xfs":
		if strings.Contains(opts, "prjquota") {
			return QuotaSupport{FSType: fs, ProjectQuotaReady: true}
		}
		return QuotaSupport{FSType: fs, Message: "xfs requires the `prjquota` mount option for project quota enforcement"}
	case "ext4":
		if strings.Contains(opts, "quota") || strings.Contains(opts, "usrquota") || strings.Contains(opts, "grpquota") {
			return QuotaSupport{FSType: fs, ProjectQuotaReady: true}
		}
		return QuotaSupport{FSType: fs, Message: "ext4 requires the `quota` mount option for project quota enforcement"}
	case "":
		return QuotaSupport{Message: "no host filesystem hint provided"}
	default:
		return QuotaSupport{FSType: fs, Message: "host filesystem does not support per-project quotas"}
	}
}

// QuotaUnsupportedError returns a typed failurecodes-backed message
// the volume runner can surface when EvaluateQuotaSupport reports a
// non-ready host.
func QuotaUnsupportedError(support QuotaSupport) error {
	return &QuotaError{Code: failurecodes.VolumeQuotaUnsupported, Message: support.Message}
}

// QuotaError carries a typed failurecodes.Code so the API layer can
// translate to the stable error envelope without rewrapping.
type QuotaError struct {
	Code    failurecodes.Code
	Message string
}

func (e *QuotaError) Error() string {
	if e.Message == "" {
		return string(e.Code)
	}
	return e.Message
}
