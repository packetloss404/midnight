// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package dns

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCloudflareProviderHTTPDoubleCRUDPaginationAndAuthentication(t *testing.T) {
	var mu sync.Mutex
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		mu.Lock()
		requests = append(requests, request.Method+" "+request.URL.RequestURI())
		mu.Unlock()
		if request.Header.Get("Authorization") != "Bearer token-value" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Query().Get("page") == "1":
			fmt.Fprint(w, `{"success":true,"result":[{"id":"one","type":"A","name":"a.example.com","content":"192.0.2.1","ttl":60},{"id":"mx","type":"MX","name":"example.com","content":"mail.example.com","ttl":300},{"id":"ns","type":"NS","name":"delegated.example.com","content":"ns.example.net","ttl":300}],"result_info":{"page":1,"total_pages":2}}`)
		case request.Method == http.MethodGet && request.URL.Query().Get("page") == "2":
			fmt.Fprint(w, `{"success":true,"result":[{"id":"two","type":"TXT","name":"_midnight.example.com","content":"value","ttl":300}],"result_info":{"page":2,"total_pages":2}}`)
		case request.Method == http.MethodPost:
			assertCloudflareBody(t, request, TypeCNAME, "api.example.com", "apps.example.com", 300)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"success":true,"result":{"id":"three","type":"CNAME","name":"api.example.com","content":"apps.example.com","ttl":300}}`)
		case request.Method == http.MethodPatch:
			assertCloudflareBody(t, request, TypeCNAME, "api.example.com", "edge.example.com", 600)
			fmt.Fprint(w, `{"success":true,"result":{"id":"three","type":"CNAME","name":"api.example.com","content":"edge.example.com","ttl":600}}`)
		case request.Method == http.MethodDelete:
			fmt.Fprint(w, `{"success":true,"result":{"id":"three"}}`)
		default:
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	provider, err := NewCloudflareProvider(CloudflareConfig{Token: "token-value", BaseURL: server.URL, AllowInsecureHTTP: true, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	records, err := provider.ListRecords(context.Background(), "zone-1")
	if err != nil || len(records) != 3 || records[1].Type != TypeNS {
		t.Fatalf("records=%#v err=%v", records, err)
	}
	created, err := provider.CreateRecord(context.Background(), "zone-1", DesiredRecord{Type: TypeCNAME, Name: "api.example.com", Content: "apps.example.com", TTL: 300})
	if err != nil || created.ID != "three" {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	updated, err := provider.UpdateRecord(context.Background(), "zone-1", "three", DesiredRecord{Type: TypeCNAME, Name: "api.example.com", Content: "edge.example.com", TTL: 600})
	if err != nil || updated.Content != "edge.example.com" {
		t.Fatalf("updated=%#v err=%v", updated, err)
	}
	if err := provider.DeleteRecord(context.Background(), "zone-1", "three"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 5 || !strings.Contains(requests[0], "per_page=100") || requests[4] != "DELETE /zones/zone-1/dns_records/three" {
		t.Fatalf("requests=%#v", requests)
	}
}

func TestCloudflareProviderRetriesTransientResponsesAndRedactsProviderMessages(t *testing.T) {
	attempts := 0
	var sleeps []time.Duration
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		switch attempts {
		case 1:
			w.Header().Set("Retry-After", "0")
			http.Error(w, "rate limit token-value secret-record-value", http.StatusTooManyRequests)
		case 2:
			http.Error(w, "token-value secret-record-value", http.StatusServiceUnavailable)
		default:
			fmt.Fprint(w, `{"success":true,"result":[],"result_info":{"page":1,"total_pages":1}}`)
		}
	}))
	defer server.Close()
	provider, err := NewCloudflareProvider(CloudflareConfig{
		Token: "token-value", BaseURL: server.URL, AllowInsecureHTTP: true, HTTPClient: server.Client(), MaxAttempts: 3,
		Backoff: func(attempt int) time.Duration { return time.Duration(attempt+1) * time.Millisecond },
		Sleep:   func(_ context.Context, delay time.Duration) error { sleeps = append(sleeps, delay); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ListRecords(context.Background(), "zone-1"); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || len(sleeps) != 2 || sleeps[0] != 0 || sleeps[1] != 2*time.Millisecond {
		t.Fatalf("attempts=%d sleeps=%v", attempts, sleeps)
	}

	failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"success":false,"errors":[{"code":1000,"message":"token-value secret-record-value"}]}`)
	}))
	defer failed.Close()
	redacting, err := NewCloudflareProvider(CloudflareConfig{Token: "token-value", BaseURL: failed.URL, AllowInsecureHTTP: true, HTTPClient: failed.Client(), MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = redacting.ListRecords(context.Background(), "zone-1")
	if err == nil || strings.Contains(err.Error(), "token-value") || strings.Contains(err.Error(), "secret-record-value") {
		t.Fatalf("unredacted error=%v", err)
	}
}

func TestCloudflareProviderRejectsInsecureAndOversizedConfiguration(t *testing.T) {
	if _, err := NewCloudflareProvider(CloudflareConfig{Token: "token", BaseURL: "http://example.test"}); err == nil {
		t.Fatal("expected insecure URL rejection")
	}
	if _, err := NewCloudflareProvider(CloudflareConfig{Token: "token", MaxAttempts: 9}); err == nil {
		t.Fatal("expected retry cap rejection")
	}
}

func assertCloudflareBody(t *testing.T, request *http.Request, recordType RecordType, name, content string, ttl int) {
	t.Helper()
	var body cloudflareRecordRequest
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Type != recordType || body.Name != name || body.Content != content || body.TTL != ttl {
		t.Fatalf("body=%#v", body)
	}
}
