// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package monitors

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/webhook"
)

type deliveryStoreFake struct {
	deliveries []Delivery
	completed  []string
	retried    []string
}

func (store *deliveryStoreFake) LeaseDeliveries(context.Context, string, time.Time, time.Duration, int) ([]Delivery, error) {
	return store.deliveries, nil
}

func (store *deliveryStoreFake) CompleteDelivery(_ context.Context, id string, _ time.Time, _ int) error {
	store.completed = append(store.completed, id)
	return nil
}

func (store *deliveryStoreFake) RetryDelivery(_ context.Context, id string, _ time.Time, _ int, _ string, _ bool) error {
	store.retried = append(store.retried, id)
	return nil
}

type secretResolverFake map[string][]byte

func (resolver secretResolverFake) ResolveSecret(_ context.Context, id string) ([]byte, error) {
	return append([]byte(nil), resolver[id]...), nil
}

type deliveryResolverFake map[string][]net.IPAddr

func (resolver deliveryResolverFake) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	return resolver[host], nil
}

func TestDeliveryWorkerSignsWebhookAndCompletesOutbox(t *testing.T) {
	now := time.Date(2026, 7, 28, 2, 3, 0, 0, time.UTC)
	signing := []byte("0123456789abcdef0123456789abcdef")
	var signature string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		signature = request.Header.Get(webhook.SignatureHeader)
		body, _ := io.ReadAll(request.Body)
		parsed, err := webhook.ParseSignature(signature)
		if err != nil {
			t.Errorf("parse signature: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := webhook.VerifyDeliverySignature(parsed, []webhook.Key{{KID: "mon-key", Material: signing}}, request.Header.Get(webhook.DeliveryHeader), body, now); err != nil {
			t.Errorf("verify signature: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	store := &deliveryStoreFake{deliveries: []Delivery{{
		ID: "mdl_1", MonitorID: "mon_1", EventType: EventFiring,
		Channel: Channel{
			ID: "mch_1", Type: "webhook", EndpointVariableID: "var_url",
			SigningVariableID: "var_signing", SigningKeyID: "mon-key",
			AllowInsecureLoopback: true,
		},
		Payload: map[string]any{"event_type": EventFiring, "monitor_id": "mon_1"},
	}}}
	worker := DeliveryWorker{
		Store: store, WorkerID: "worker_1", Clock: func() time.Time { return now },
		Secrets: secretResolverFake{"var_url": []byte(server.URL), "var_signing": signing},
	}
	completed, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if completed != 1 || len(store.completed) != 1 || len(store.retried) != 0 {
		t.Fatalf("completed = %d, store = %#v", completed, store)
	}
	if signature == "" {
		t.Fatal("signature was not sent")
	}
}

func TestDeliveryWorkerRetriesWithoutPersistingEndpoint(t *testing.T) {
	store := &deliveryStoreFake{deliveries: []Delivery{{
		ID: "mdl_1", MonitorID: "mon_1", EventType: EventFiring,
		Channel: Channel{
			ID: "mch_1", Type: "webhook", EndpointVariableID: "var_url",
			SigningVariableID: "var_signing",
		},
		Payload: map[string]any{"monitor_id": "mon_1"},
	}}}
	worker := DeliveryWorker{
		Store: store,
		Secrets: secretResolverFake{
			"var_url":     []byte("http://example.com/secret?token=leak"),
			"var_signing": []byte("0123456789abcdef0123456789abcdef"),
		},
	}
	completed, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if completed != 0 || len(store.retried) != 1 {
		t.Fatalf("completed = %d, store = %#v", completed, store)
	}
}

func TestDeliveryWorkerDoesNotFollowWebhookRedirects(t *testing.T) {
	var initialRequests atomic.Int64
	var redirectedRequests atomic.Int64
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectedRequests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer redirectTarget.Close()
	initial := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		initialRequests.Add(1)
		http.Redirect(w, request, redirectTarget.URL, http.StatusTemporaryRedirect)
	}))
	defer initial.Close()

	store := &deliveryStoreFake{deliveries: []Delivery{{
		ID: "mdl_redirect", MonitorID: "mon_1", EventType: EventFiring,
		Channel: Channel{
			ID: "mch_1", Type: "webhook", EndpointVariableID: "var_url",
			SigningVariableID:     "var_signing",
			AllowInsecureLoopback: true,
		},
		Payload: map[string]any{"monitor_id": "mon_1"},
	}}}
	worker := DeliveryWorker{
		Store: store,
		Secrets: secretResolverFake{
			"var_url":     []byte(initial.URL),
			"var_signing": []byte("0123456789abcdef0123456789abcdef"),
		},
		Client: initial.Client(),
	}

	completed, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if completed != 0 || len(store.retried) != 1 {
		t.Fatalf("completed = %d, store = %#v", completed, store)
	}
	if got := initialRequests.Load(); got != 1 {
		t.Fatalf("initial target received %d requests, want 1", got)
	}
	if got := redirectedRequests.Load(); got != 0 {
		t.Fatalf("redirect target received %d signed webhook requests, want 0", got)
	}
}

func TestValidateWebhookEndpointRejectsLiteralPrivateAddress(t *testing.T) {
	if _, err := validateWebhookEndpoint("https://10.23.45.67/hooks", false); err == nil {
		t.Fatal("private literal endpoint was accepted")
	}
}

func TestDeliveryWorkerRejectsLiteralLoopbackInProduction(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	store := &deliveryStoreFake{deliveries: []Delivery{{
		ID: "mdl_literal", MonitorID: "mon_1", EventType: EventFiring,
		Channel: Channel{
			ID: "mch_1", Type: "webhook", EndpointVariableID: "var_url",
			SigningVariableID: "var_signing",
		},
		Payload: map[string]any{"monitor_id": "mon_1"},
	}}}
	worker := DeliveryWorker{
		Store: store,
		Secrets: secretResolverFake{
			"var_url":     []byte(server.URL),
			"var_signing": []byte("0123456789abcdef0123456789abcdef"),
		},
		Client: server.Client(),
	}
	completed, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if completed != 0 || len(store.retried) != 1 {
		t.Fatalf("completed = %d, store = %#v", completed, store)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("loopback target received %d requests, want 0", got)
	}
}

func TestDeliveryWorkerRejectsPrivateDNSAnswerBeforeConnect(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	endpoint := endpointWithHost(t, server.URL, "example.com")

	store := &deliveryStoreFake{deliveries: []Delivery{{
		ID: "mdl_dns", MonitorID: "mon_1", EventType: EventFiring,
		Channel: Channel{
			ID: "mch_1", Type: "webhook", EndpointVariableID: "var_url",
			SigningVariableID: "var_signing",
		},
		Payload: map[string]any{"monitor_id": "mon_1"},
	}}}
	worker := DeliveryWorker{
		Store: store,
		Secrets: secretResolverFake{
			"var_url":     []byte(endpoint),
			"var_signing": []byte("0123456789abcdef0123456789abcdef"),
		},
		Client:   server.Client(),
		Resolver: deliveryResolverFake{"example.com": {{IP: net.ParseIP("127.0.0.1")}}},
	}
	completed, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if completed != 0 || len(store.retried) != 1 {
		t.Fatalf("completed = %d, store = %#v", completed, store)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("private DNS target received %d requests, want 0", got)
	}
}

func TestDeliveryWorkerPinsAddressButVerifiesOriginalTLSHostname(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	endpoint := endpointWithHost(t, server.URL, "example.com")
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	verifiedClient := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
	}}

	store := &deliveryStoreFake{deliveries: []Delivery{{
		ID: "mdl_tls", MonitorID: "mon_1", EventType: EventFiring,
		Channel: Channel{
			ID: "mch_1", Type: "webhook", EndpointVariableID: "var_url",
			SigningVariableID: "var_signing", AllowInsecureLoopback: true,
		},
		Payload: map[string]any{"monitor_id": "mon_1"},
	}}}
	worker := DeliveryWorker{
		Store: store,
		Secrets: secretResolverFake{
			"var_url":     []byte(endpoint),
			"var_signing": []byte("0123456789abcdef0123456789abcdef"),
		},
		Client:   verifiedClient,
		Resolver: deliveryResolverFake{"example.com": {{IP: net.ParseIP("127.0.0.1")}}},
	}
	completed, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if completed != 1 || len(store.completed) != 1 || len(store.retried) != 0 {
		t.Fatalf("completed = %d, store = %#v", completed, store)
	}
}

func endpointWithHost(t *testing.T, raw, hostname string) string {
	t.Helper()
	endpoint, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	endpoint.Host = net.JoinHostPort(hostname, endpoint.Port())
	return endpoint.String()
}
