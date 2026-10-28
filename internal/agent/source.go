package agent

import (
	"context"
	"errors"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxSourceFiles      = 64
	MaxSourceFileBytes  = 128 << 10
	MaxSourceTotalBytes = 512 << 10
	MaxSourcePathBytes  = 240
)

// SourceDeployRequest contains a bounded text-only source tree, never a host
// filesystem path, clone URL, credential, or provider authority coordinate.
// RequestKey is stable across retries and must change for a new logical build.
type SourceDeployRequest struct {
	ServiceID                  string            `json:"service_id"`
	EnvironmentID              string            `json:"environment_id"`
	RequestKey                 string            `json:"request_key"`
	Files                      map[string]string `json:"files"`
	ExpectedServiceVersion     int64             `json:"expected_service_version"`
	ExpectedEnvironmentVersion int64             `json:"expected_environment_version"`
	ExpectedConfigVersion      int64             `json:"expected_config_version"`
}

// ScopedBuildIntake is the tenant-safe replacement for the legacy repo/ref
// interface. Adapters implementing it must independently authorize the live
// human and canonical target before publishing source or accepting work.
type ScopedBuildIntake interface {
	DeployScopedSource(context.Context, SourceDeployRequest) (Deployment, error)
}

// SourceDeploymentContext is a current, non-secret target snapshot. The model
// must copy these exact versions into deploy_from_source, never invent them.
type SourceDeploymentContext struct {
	ServiceID                  string `json:"service_id"`
	EnvironmentID              string `json:"environment_id"`
	ExpectedServiceVersion     int64  `json:"expected_service_version"`
	ExpectedEnvironmentVersion int64  `json:"expected_environment_version"`
	ExpectedConfigVersion      int64  `json:"expected_config_version"`
}
type ScopedSourceContext interface {
	SourceDeploymentContext(context.Context, string, string) (SourceDeploymentContext, error)
}

var sourceRequestKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var sourceTargetID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)

func ValidateSourceDeployRequest(request SourceDeployRequest) error {
	if !sourceTargetID.MatchString(request.ServiceID) || !sourceTargetID.MatchString(request.EnvironmentID) || !sourceRequestKey.MatchString(request.RequestKey) {
		return errors.New("deploy_from_source: exact service_id, environment_id, and request_key are required")
	}
	if len(request.Files) == 0 || len(request.Files) > MaxSourceFiles {
		return errors.New("deploy_from_source: files must contain 1 to 64 text files")
	}
	if request.ExpectedServiceVersion < 1 || request.ExpectedEnvironmentVersion < 1 || request.ExpectedConfigVersion < 0 {
		return errors.New("deploy_from_source: current service/environment versions and a nonnegative config version are required")
	}
	total := 0
	for name, content := range request.Files {
		if name == "" || len(name) > MaxSourcePathBytes || !utf8.ValidString(name) || path.IsAbs(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\:\x00\r\n\t") || strings.Count(name, "/") >= 8 {
			return errors.New("deploy_from_source: file paths must be bounded relative POSIX paths")
		}
		for _, char := range name {
			if unicode.IsControl(char) || unicode.Is(unicode.Cf, char) {
				return errors.New("deploy_from_source: control characters in file paths are not allowed")
			}
		}
		for _, component := range strings.Split(name, "/") {
			if component == "." || component == ".." || strings.TrimSpace(component) != component || reservedSourceComponent(component) {
				return errors.New("deploy_from_source: reserved or traversing file path")
			}
		}
		if len(content) > MaxSourceFileBytes || !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
			return errors.New("deploy_from_source: files must be UTF-8 text without NUL and at most 128 KiB each")
		}
		total += len(content)
		if total > MaxSourceTotalBytes {
			return errors.New("deploy_from_source: source text exceeds 512 KiB")
		}
	}
	return nil
}

func reservedSourceComponent(component string) bool {
	name := strings.ToLower(component)
	if strings.HasPrefix(name, ".env") {
		return true
	}
	switch name {
	case ".git", ".midnight", ".ssh", ".aws", ".azure", ".kube", ".config", ".npmrc", ".netrc", ".git-credentials", "id_rsa", "id_ed25519":
		return true
	}
	return false
}

type sourceConversationKey struct{}

// WithSourceConversationID carries only the server-authorized conversation
// identity. The accepting coordinator still verifies its durable ownership.
func WithSourceConversationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sourceConversationKey{}, id)
}
func SourceConversationID(ctx context.Context) string {
	id, _ := ctx.Value(sourceConversationKey{}).(string)
	return id
}
