package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// PushEvent is the panel-locked subset of the GitHub push event
// payload Lane 4 promotes to a Midnight deploy operation. Fields
// outside this subset are ignored.
type PushEvent struct {
	Ref            string
	Branch         string
	Before         string
	After          string
	HeadSHA        string
	InstallationID int64
	Deleted        bool
	Repository     struct {
		ID       int64
		FullName string
		Private  bool
		Fork     bool
	}
	Pusher struct {
		Login string
		Email string
	}
	HeadCommit struct {
		ID        string
		Message   string
		Timestamp time.Time
	}
	// Untrusted is true when the push originates from a fork. The router
	// currently skips these events until the build path can guarantee preview
	// isolation with no project secrets or shared cache.
	Untrusted bool
}

// ErrPushEventInvalid is returned when ParsePushEvent receives a
// payload that does not match the panel-locked subset.
var ErrPushEventInvalid = errors.New("github: push event invalid")

// ParsePushEvent decodes the GitHub `push` webhook body. Returns the
// panel-locked subset of fields plus the Untrusted flag.
func ParsePushEvent(body []byte) (PushEvent, error) {
	var raw struct {
		Ref          string `json:"ref"`
		Before       string `json:"before"`
		After        string `json:"after"`
		Deleted      bool   `json:"deleted"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		Repository struct {
			ID       int64  `json:"id"`
			FullName string `json:"full_name"`
			Private  bool   `json:"private"`
			Fork     bool   `json:"fork"`
		} `json:"repository"`
		Pusher struct {
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"pusher"`
		HeadCommit struct {
			ID        string    `json:"id"`
			Message   string    `json:"message"`
			Timestamp time.Time `json:"timestamp"`
		} `json:"head_commit"`
		Sender struct {
			Type string `json:"type"`
		} `json:"sender"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return PushEvent{}, fmt.Errorf("%w: %v", ErrPushEventInvalid, err)
	}
	if raw.Installation.ID <= 0 || raw.Repository.ID <= 0 {
		return PushEvent{}, fmt.Errorf("%w: positive installation.id and repository.id required", ErrPushEventInvalid)
	}
	if raw.Repository.FullName == "" || !validRepositoryFullName(raw.Repository.FullName) {
		return PushEvent{}, fmt.Errorf("%w: repository.full_name must be owner/name", ErrPushEventInvalid)
	}
	const branchRefPrefix = "refs/heads/"
	if !strings.HasPrefix(raw.Ref, branchRefPrefix) {
		return PushEvent{}, fmt.Errorf("%w: ref must identify a branch", ErrPushEventInvalid)
	}
	branch := strings.TrimPrefix(raw.Ref, branchRefPrefix)
	if !validBranchName(branch) {
		return PushEvent{}, fmt.Errorf("%w: branch ref invalid", ErrPushEventInvalid)
	}
	if (!raw.Deleted && !validCommitSHA(raw.After)) ||
		(raw.Deleted && !zeroCommitSHA(raw.After)) {
		return PushEvent{}, fmt.Errorf("%w: after must be a nonzero 40- or 64-hex commit id", ErrPushEventInvalid)
	}
	if raw.Sender.Type == "" || containsControl(raw.Sender.Type) {
		return PushEvent{}, fmt.Errorf("%w: sender.type required", ErrPushEventInvalid)
	}
	out := PushEvent{
		Ref:            raw.Ref,
		Branch:         branch,
		Before:         raw.Before,
		After:          raw.After,
		HeadSHA:        raw.After,
		InstallationID: raw.Installation.ID,
		Deleted:        raw.Deleted,
	}
	out.Repository.ID = raw.Repository.ID
	out.Repository.FullName = raw.Repository.FullName
	out.Repository.Private = raw.Repository.Private
	out.Repository.Fork = raw.Repository.Fork
	out.Pusher.Login = raw.Pusher.Name
	out.Pusher.Email = raw.Pusher.Email
	out.HeadCommit.ID = raw.HeadCommit.ID
	out.HeadCommit.Message = raw.HeadCommit.Message
	out.HeadCommit.Timestamp = raw.HeadCommit.Timestamp
	out.Untrusted = raw.Repository.Fork
	return out, nil
}

func zeroCommitSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, c := range value {
		if c != '0' {
			return false
		}
	}
	return true
}

func validCommitSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	nonzero := false
	for _, c := range value {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
		if c != '0' {
			nonzero = true
		}
	}
	return nonzero
}

func validRepositoryFullName(value string) bool {
	if value != strings.TrimSpace(value) || containsControl(value) {
		return false
	}
	parts := strings.Split(value, "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != ""
}

func validBranchName(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || containsControl(value) {
		return false
	}
	// These are the ref-name hazards relevant to an exact refs/heads binding.
	// GitHub already validates refs, but rejecting them here prevents malformed
	// synthetic payloads from becoming deploy source selectors.
	return !strings.HasPrefix(value, "/") &&
		!strings.HasSuffix(value, "/") &&
		!strings.HasSuffix(value, ".") &&
		!strings.Contains(value, "..") &&
		!strings.Contains(value, "@{") &&
		!strings.ContainsAny(value, `~^:?*[\`)
}

func containsControl(value string) bool {
	for _, c := range value {
		if c < 0x20 || c == 0x7f {
			return true
		}
	}
	return false
}

// DeployRequest is the panel-locked shape Lane 4 derives from a push
// event. Lane 1's API client converts this into a POST against
// /api/v1alpha/services/{service_id}/deploy.
type DeployRequest struct {
	Provider    string `json:"provider"`
	Repo        string `json:"repo"`
	Ref         string `json:"ref"`
	SHA         string `json:"sha"`
	Untrusted   bool   `json:"untrusted"`
	Pusher      string `json:"pusher,omitempty"`
	Message     string `json:"commit_message,omitempty"`
	Idempotency string `json:"idempotency_key,omitempty"`
}

// PushEventToDeploy returns the DeployRequest a push event maps to.
// idempotencyKey is the panel-locked idempotency token the API client
// passes via the Idempotency-Key header so concurrent push events for
// the same SHA collapse to a single deploy.
func PushEventToDeploy(ev PushEvent) DeployRequest {
	return DeployRequest{
		Provider:    "github",
		Repo:        ev.Repository.FullName,
		Ref:         ev.Ref,
		SHA:         ev.HeadSHA,
		Untrusted:   ev.Untrusted,
		Pusher:      ev.Pusher.Login,
		Message:     ev.HeadCommit.Message,
		Idempotency: "gh-push-" + ev.HeadSHA,
	}
}

// APIClient is the boundary the dispatcher calls to create the deploy
// operation. Lane 4 deliberately avoids hardcoding router internals;
// callers wire a real HTTP client. The default in this package is a
// stub.
type APIClient interface {
	CreateDeploy(ctx context.Context, req DeployRequest) (operationID string, err error)
}

// StubAPIClient is the default APIClient used when no real client is
// wired. It returns ErrLiveAPIDisabled.
type StubAPIClient struct{}

// CreateDeploy implements APIClient.
func (StubAPIClient) CreateDeploy(_ context.Context, _ DeployRequest) (string, error) {
	return "", ErrLiveAPIDisabled
}

// ErrLiveAPIDisabled is returned when a live deploy creation is
// attempted without a real APIClient wired.
var ErrLiveAPIDisabled = errors.New("github: live API client not wired (set in cmd/midnight-server bootstrap)")
