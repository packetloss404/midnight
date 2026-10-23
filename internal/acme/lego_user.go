package acme

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-acme/lego/v4/registration"
)

// AccountUser implements lego's registration.User contract. It is the
// platform-side representation of an ACME account: an email plus a
// persistent ECDSA P-256 private key. The private key is the long-lived
// secret that authenticates every ACME order; rotating it invalidates the
// account at the CA.
//
// Phase 1 Lane 3 stores the account key on disk as PEM at the path supplied
// by NewAccountUser. When secrets land in Lane 4 the key migrates into the
// envelope-encrypted secret store and this struct gains an explicit
// `KeyProvider` seam.
type AccountUser struct {
	email        string
	privateKey   crypto.PrivateKey
	registration *registration.Resource
}

// NewAccountUser loads (or creates) an ECDSA P-256 ACME account key under
// keyPath. The directory is created `0700` if missing and the key file is
// created `0600`.
func NewAccountUser(email, keyPath string) (*AccountUser, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, errors.New("ACME account email is required")
	}
	if !filepath.IsAbs(keyPath) {
		return nil, errors.New("ACME account key path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return nil, fmt.Errorf("create ACME state directory: %w", err)
	}
	key, err := loadOrCreateAccountKey(keyPath)
	if err != nil {
		return nil, err
	}
	return &AccountUser{email: email, privateKey: key}, nil
}

// GetEmail returns the operator-supplied account email.
func (u *AccountUser) GetEmail() string {
	if u == nil {
		return ""
	}
	return u.email
}

// GetRegistration returns the cached registration resource. Callers must
// have invoked Register or LoadRegistration first.
func (u *AccountUser) GetRegistration() *registration.Resource {
	if u == nil {
		return nil
	}
	return u.registration
}

// SetRegistration records a freshly created or restored registration
// resource on the account.
func (u *AccountUser) SetRegistration(r *registration.Resource) {
	if u == nil {
		return
	}
	u.registration = r
}

// GetPrivateKey returns the ECDSA private key that authenticates the
// account. It implements registration.User.
func (u *AccountUser) GetPrivateKey() crypto.PrivateKey {
	if u == nil {
		return nil
	}
	return u.privateKey
}

func loadOrCreateAccountKey(path string) (*ecdsa.PrivateKey, error) {
	if data, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(data)
		if block == nil || block.Type != "EC PRIVATE KEY" {
			return nil, fmt.Errorf("ACME account key %s is not an EC PRIVATE KEY PEM", path)
		}
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse ACME account key: %w", err)
		}
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read ACME account key: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ACME account key: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal ACME account key: %w", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	tmp, err := os.CreateTemp(filepath.Dir(path), ".acme-account-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("write ACME account key: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(pemBytes); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("write ACME account key body: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("chmod ACME account key: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close ACME account key: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return nil, fmt.Errorf("rename ACME account key: %w", err)
	}
	return key, nil
}
