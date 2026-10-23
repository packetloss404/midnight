// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package monitors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/ssrf"
	"github.com/midnight/midnight/internal/webhook"
)

type DeliveryWorker struct {
	Store     DeliveryStore
	Secrets   SecretResolver
	Client    *http.Client
	WorkerID  string
	Clock     func() time.Time
	Lease     time.Duration
	BatchSize int
	Resolver  ssrf.Resolver
}

func (worker DeliveryWorker) RunOnce(ctx context.Context) (int, error) {
	if worker.Store == nil || worker.Secrets == nil {
		return 0, errors.New("monitor delivery worker requires store and secret resolver")
	}
	now := worker.now()
	deliveries, err := worker.Store.LeaseDeliveries(ctx, worker.WorkerID, now, worker.lease(), worker.batchSize())
	if err != nil {
		return 0, fmt.Errorf("lease monitor deliveries: %w", err)
	}
	completed := 0
	for _, delivery := range deliveries {
		status, deliverErr := worker.deliver(ctx, delivery, now)
		attempts := delivery.Attempts + 1
		if deliverErr == nil {
			if err := worker.Store.CompleteDelivery(ctx, delivery.ID, now, status); err != nil {
				return completed, err
			}
			completed++
			continue
		}
		dead := attempts >= MaxDeliveryAttempts
		next := now.Add(deliveryBackoff(attempts))
		if err := worker.Store.RetryDelivery(ctx, delivery.ID, next, status, redactDeliveryError(deliverErr), dead); err != nil {
			return completed, err
		}
	}
	return completed, nil
}

func (worker DeliveryWorker) deliver(ctx context.Context, delivery Delivery, now time.Time) (int, error) {
	if delivery.Channel.Type != "webhook" {
		return 0, errors.New("unsupported monitor delivery channel")
	}
	endpointBytes, err := worker.Secrets.ResolveSecret(ctx, delivery.Channel.EndpointVariableID)
	if err != nil {
		return 0, errors.New("resolve endpoint secret")
	}
	defer zeroBytes(endpointBytes)
	signingBytes, err := worker.Secrets.ResolveSecret(ctx, delivery.Channel.SigningVariableID)
	if err != nil {
		return 0, errors.New("resolve signing secret")
	}
	defer zeroBytes(signingBytes)

	endpoint, err := validateWebhookEndpoint(string(endpointBytes), delivery.Channel.AllowInsecureLoopback)
	if err != nil {
		return 0, err
	}
	body, err := json.Marshal(delivery.Payload)
	if err != nil {
		return 0, errors.New("encode monitor payload")
	}
	keyID := strings.TrimSpace(delivery.Channel.SigningKeyID)
	if keyID == "" {
		keyID = "monitor"
	}
	signature, err := (webhook.Signer{}).SignDelivery([]webhook.Key{{KID: keyID, Material: signingBytes}}, delivery.ID, body, now)
	if err != nil {
		return 0, errors.New("sign monitor webhook")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return 0, errors.New("construct monitor webhook")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(webhook.SignatureHeader, signature)
	request.Header.Set(webhook.DeliveryHeader, delivery.ID)
	request.Header.Set(webhook.EventIDHeader, delivery.MonitorID)
	request.Header.Set(webhook.EventTypeHeader, delivery.EventType)
	client, err := worker.client(endpoint, delivery.Channel.AllowInsecureLoopback)
	if err != nil {
		return 0, err
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, errors.New("monitor webhook request failed")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, fmt.Errorf("monitor webhook returned status %d", response.StatusCode)
	}
	return response.StatusCode, nil
}

func validateWebhookEndpoint(raw string, allowInsecureLoopback bool) (*url.URL, error) {
	endpoint, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("monitor webhook endpoint is invalid")
	}
	host := endpoint.Hostname()
	if address, parseErr := netip.ParseAddr(strings.Trim(host, "[]")); parseErr == nil {
		if err := ssrf.ValidateAddress(address, allowInsecureLoopback); err != nil {
			return nil, errors.New("monitor webhook endpoint is not public")
		}
	}
	if endpoint.Scheme == "https" {
		return endpoint, nil
	}
	ip := net.ParseIP(host)
	if allowInsecureLoopback && endpoint.Scheme == "http" &&
		(strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())) {
		return endpoint, nil
	}
	return nil, errors.New("monitor webhook endpoint must use https")
}

func deliveryBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 8 {
		attempt = 8
	}
	return time.Duration(1<<uint(attempt-1)) * 30 * time.Second
}

func redactDeliveryError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func (worker DeliveryWorker) now() time.Time {
	if worker.Clock != nil {
		return worker.Clock().UTC()
	}
	return time.Now().UTC()
}

func (worker DeliveryWorker) lease() time.Duration {
	if worker.Lease > 0 {
		return worker.Lease
	}
	return DefaultLease
}

func (worker DeliveryWorker) batchSize() int {
	if worker.BatchSize > 0 && worker.BatchSize <= 100 {
		return worker.BatchSize
	}
	return 25
}

func (worker DeliveryWorker) client(endpoint *url.URL, allowInsecureLoopback bool) (*http.Client, error) {
	var client http.Client
	if worker.Client != nil {
		client = *worker.Client
	} else {
		client.Timeout = 15 * time.Second
	}
	var transport *http.Transport
	switch configured := client.Transport.(type) {
	case nil:
		transport = http.DefaultTransport.(*http.Transport).Clone()
	case *http.Transport:
		transport = configured.Clone()
	default:
		return nil, errors.New("monitor webhook client requires an HTTP transport")
	}
	// Webhooks are always delivered directly. A configured proxy or custom TLS
	// dial hook would otherwise be able to bypass endpoint resolution and the
	// pinned-address boundary below.
	transport.Proxy = nil
	transport.DialTLS = nil
	transport.DialTLSContext = nil
	transport.DialContext = ssrf.Dialer{
		Resolver:      worker.Resolver,
		ExpectedHost:  endpoint.Hostname(),
		AllowLoopback: allowInsecureLoopback,
	}.Dial
	client.Transport = transport
	// A validated HTTPS destination must not redirect the signed request to a
	// different origin or an otherwise-disallowed scheme. Returning the 3xx
	// response lets the delivery loop record a bounded, redacted failure.
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &client, nil
}
