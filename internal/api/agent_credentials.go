package api

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const MinAgentCredentialBytes = 32

// AgentCredentialResolver returns the currently accepted credentials for one
// runtime agent. Multiple values provide a bounded overlap window during
// credential rotation; callers must remove the old value once rollout is
// complete.
type AgentCredentialResolver interface {
	ResolveAgentCredentials(agentID string) [][]byte
}

// StaticAgentCredentials is the production credential resolver loaded from an
// operator-owned file. Credentials are copied on construction and lookup so
// callers cannot mutate the active key set.
type StaticAgentCredentials struct {
	agents map[string][][]byte
}

// NewStaticAgentCredentials validates and copies a per-agent credential map.
func NewStaticAgentCredentials(agents map[string][]string) (*StaticAgentCredentials, error) {
	if len(agents) == 0 {
		return nil, errors.New("agent credentials: at least one agent is required")
	}
	out := &StaticAgentCredentials{agents: make(map[string][][]byte, len(agents))}
	credentialOwner := make(map[string]string)
	for rawAgentID, rawCredentials := range agents {
		agentID := strings.TrimSpace(rawAgentID)
		if agentID == "" || strings.Contains(agentID, "/") {
			return nil, fmt.Errorf("agent credentials: invalid agent id %q", rawAgentID)
		}
		if len(rawCredentials) == 0 {
			return nil, fmt.Errorf("agent credentials: agent %q has no credentials", agentID)
		}
		if _, duplicate := out.agents[agentID]; duplicate {
			return nil, fmt.Errorf("agent credentials: duplicate normalized agent id %q", agentID)
		}
		out.agents[agentID] = nil
		seen := make(map[string]struct{}, len(rawCredentials))
		for _, rawCredential := range rawCredentials {
			credential := strings.TrimSpace(rawCredential)
			if len(credential) < MinAgentCredentialBytes {
				return nil, fmt.Errorf("agent credentials: credential for agent %q yields %d bytes, want >= %d", agentID, len(credential), MinAgentCredentialBytes)
			}
			if strings.ContainsAny(credential, " \t\r\n,") {
				return nil, fmt.Errorf("agent credentials: credential for agent %q contains HTTP bearer delimiters", agentID)
			}
			if _, duplicate := seen[credential]; duplicate {
				continue
			}
			if owner := credentialOwner[credential]; owner != "" && owner != agentID {
				return nil, fmt.Errorf("agent credentials: credential material is reused by agents %q and %q", owner, agentID)
			}
			credentialOwner[credential] = agentID
			seen[credential] = struct{}{}
			out.agents[agentID] = append(out.agents[agentID], []byte(credential))
		}
	}
	return out, nil
}

func (s *StaticAgentCredentials) ResolveAgentCredentials(agentID string) [][]byte {
	if s == nil {
		return nil
	}
	values := s.agents[strings.TrimSpace(agentID)]
	out := make([][]byte, 0, len(values))
	for _, value := range values {
		out = append(out, append([]byte(nil), value...))
	}
	return out
}

type agentCredentialFile struct {
	Version int                 `json:"version"`
	Agents  map[string][]string `json:"agents"`
}

// LoadAgentCredentialFile loads the production per-agent keyring. The file is
// intentionally simple so it can be supplied by systemd credentials, Docker
// secrets, or a secrets manager sidecar without database bootstrapping:
//
//	{"version":1,"agents":{"agent-a":["<credential>"]}}
func LoadAgentCredentialFile(path string) (*StaticAgentCredentials, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("agent credentials: file path is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("agent credentials: stat file: %w", err)
	}
	if info.Mode().Perm()&0o027 != 0 {
		return nil, fmt.Errorf("agent credentials: file permissions %04o expose or allow mutation outside the owning user/group", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("agent credentials: read file: %w", err)
	}
	var document agentCredentialFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("agent credentials: decode file: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("agent credentials: file contains trailing data")
	}
	if document.Version != 1 {
		return nil, fmt.Errorf("agent credentials: unsupported version %d", document.Version)
	}
	return NewStaticAgentCredentials(document.Agents)
}

// developmentAgentCredentials deliberately accepts one shared key for any
// agent ID. It exists only for explicit local-development boot paths and must
// never be selected by production startup.
type developmentAgentCredentials struct {
	credential []byte
}

// NewDevelopmentAgentCredentials creates the explicit local-development
// compatibility resolver used by unit tests and --agent-auth-development.
func NewDevelopmentAgentCredentials(credential []byte) AgentCredentialResolver {
	return &developmentAgentCredentials{credential: append([]byte(nil), credential...)}
}

func (d *developmentAgentCredentials) ResolveAgentCredentials(agentID string) [][]byte {
	if d == nil || strings.TrimSpace(agentID) == "" || len(d.credential) == 0 {
		return nil
	}
	return [][]byte{append([]byte(nil), d.credential...)}
}

func matchAgentCredential(resolver AgentCredentialResolver, agentID, supplied string) ([]byte, bool) {
	suppliedBytes := []byte(supplied)
	var matched []byte
	found := 0
	for _, credential := range resolver.ResolveAgentCredentials(agentID) {
		if len(credential) != len(suppliedBytes) {
			continue
		}
		if subtle.ConstantTimeCompare(credential, suppliedBytes) == 1 {
			matched = append([]byte(nil), credential...)
			found = 1
		}
	}
	return matched, found == 1
}
