package desiredstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CacheFile stores the last desired-state payload and validator at an explicit path.
type CacheFile struct {
	Path string
}

// Load reads cached desired state. A missing cache returns an empty State.
func (c CacheFile) Load() (State, error) {
	path, err := c.path()
	if err != nil {
		return State{}, err
	}
	payload, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("read desired-state cache: %w", err)
	}
	var state State
	if err := json.Unmarshal(payload, &state); err != nil {
		return State{}, fmt.Errorf("decode desired-state cache: %w", err)
	}
	return state, nil
}

// Save writes desired state to the explicit cache path.
func (c CacheFile) Save(state State) error {
	path, err := c.path()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode desired-state cache: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create desired-state cache dir: %w", err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return fmt.Errorf("write desired-state cache: %w", err)
	}
	return nil
}

func (c CacheFile) path() (string, error) {
	path := strings.TrimSpace(c.Path)
	if path == "" {
		return "", fmt.Errorf("cache path is required")
	}
	return path, nil
}
