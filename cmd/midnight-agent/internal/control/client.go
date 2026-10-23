// Package control wires the agent's HTTPS+HMAC connection to the
// midnight-server control plane. It composes the protocol package's request
// builder with an HTTPS-capable HTTP/2 transport and the panel-locked HMAC
// heartbeat helper.
//
// Lane 2 ships HTTPS+HMAC (already-locked HMAC helper). mTLS is a stretch
// follow-on tracked in agile/handoff/phase1-subagents/LANE2_STATUS.md.
package control

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/net/http2"

	"github.com/midnight/midnight/cmd/midnight-agent/internal/protocol"
)

// HTTPClientConfig captures the agent's outbound HTTP transport options.
type HTTPClientConfig struct {
	// CABundlePath is a PEM file containing the control plane's CA. If
	// empty the agent uses the system trust store. mTLS is gated by both
	// ClientCertPath and ClientKeyPath being set.
	CABundlePath   string
	ClientCertPath string
	ClientKeyPath  string
	// Insecure disables certificate verification. Allowed only when the
	// server URL is http:// or for local-development integration tests;
	// the agent main loop refuses Insecure=true with an https:// URL.
	Insecure bool
	// DialTimeout caps connection setup. Defaults to 10s.
	DialTimeout time.Duration
	// RequestTimeout caps per-request duration. Defaults to 30s. The
	// heartbeat loop sets a tighter ceiling via context.
	RequestTimeout time.Duration
}

// NewHTTPClient builds the agent's outbound HTTP client. HTTPS-equipped
// servers receive an HTTP/2-capable transport with TLS verification (mTLS
// when client cert paths are set); plain HTTP servers (developer mode) get a
// vanilla HTTP/1.1 client.
func NewHTTPClient(cfg HTTPClientConfig, serverURL string) (*http.Client, error) {
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 30 * time.Second
	}

	dialer := &net.Dialer{Timeout: cfg.DialTimeout, KeepAlive: 30 * time.Second}

	if !strings.HasPrefix(strings.ToLower(serverURL), "https://") {
		// Developer mode: HTTP/1.1, no TLS. We still wrap the client in a
		// timeout so a wedged server doesn't hang the heartbeat loop.
		return &http.Client{
			Timeout: cfg.RequestTimeout,
			Transport: &http.Transport{
				DialContext:           dialer.DialContext,
				MaxIdleConns:          16,
				IdleConnTimeout:       60 * time.Second,
				TLSHandshakeTimeout:   cfg.DialTimeout,
				ExpectContinueTimeout: 1 * time.Second,
			},
		}, nil
	}

	tlsCfg, err := buildTLSConfig(cfg)
	if err != nil {
		return nil, err
	}

	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		TLSClientConfig:       tlsCfg,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   cfg.DialTimeout,
		ExpectContinueTimeout: 1 * time.Second,
	}
	if err := http2.ConfigureTransport(transport); err != nil {
		return nil, fmt.Errorf("configure http2: %w", err)
	}

	return &http.Client{
		Timeout:   cfg.RequestTimeout,
		Transport: transport,
	}, nil
}

func buildTLSConfig(cfg HTTPClientConfig) (*tls.Config, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}}

	if cfg.Insecure {
		tlsCfg.InsecureSkipVerify = true
	}
	if cfg.CABundlePath != "" {
		pem, err := os.ReadFile(cfg.CABundlePath)
		if err != nil {
			return nil, fmt.Errorf("read ca bundle: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("ca bundle contains no valid PEM certificates")
		}
		tlsCfg.RootCAs = pool
	}
	if cfg.ClientCertPath != "" || cfg.ClientKeyPath != "" {
		if cfg.ClientCertPath == "" || cfg.ClientKeyPath == "" {
			return nil, errors.New("mTLS requires both ClientCertPath and ClientKeyPath")
		}
		cert, err := tls.LoadX509KeyPair(cfg.ClientCertPath, cfg.ClientKeyPath)
		if err != nil {
			return nil, fmt.Errorf("load client keypair: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}
	return tlsCfg, nil
}

// NewAgentClient wires a protocol.AgentClient backed by the supplied
// http.Client. token is the credential scoped to this exact agent ID; it is
// independent from the installation operator recovery bearer. mTLS may be
// layered on without widening the credential's authority.
func NewAgentClient(httpClient *http.Client, baseURL, token, userAgent string) protocol.AgentClient {
	return protocol.AgentClient{
		Builder: protocol.RequestBuilder{
			BaseURL:   baseURL,
			Token:     token,
			UserAgent: userAgent,
		},
		Executor: protocol.Client{Doer: httpClient},
	}
}
