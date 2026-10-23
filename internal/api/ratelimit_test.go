package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/idempotency"
)

func TestAPIAdmissionEnforcesSharedRequestBudgetAndIgnoresSpoofedForwarding(t *testing.T) {
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	controller := NewAPIAdmissionController(APIAdmissionLimits{
		RequestsPerWindow: 2,
		Window:            time.Minute,
		SSEConcurrent:     2,
	}, func() time.Time { return now })
	handler := controller.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	serve := func(actor idempotency.Actor, remote, forwarded string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations", nil)
		request.RemoteAddr = remote
		request.Header.Set("X-Forwarded-For", forwarded)
		ctx := idempotency.WithActor(context.Background(), actor)
		ctx = idempotency.WithTenant(ctx, "tenant-a")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request.WithContext(ctx))
		return response
	}

	first := serve("user:a", "192.0.2.10:443", "198.51.100.1")
	second := serve("user:a", "192.0.2.10:443", "198.51.100.2")
	third := serve("user:a", "192.0.2.10:443", "203.0.113.9")
	if first.Code != http.StatusNoContent || second.Code != http.StatusNoContent || third.Code != http.StatusTooManyRequests {
		t.Fatalf("statuses = %d, %d, %d", first.Code, second.Code, third.Code)
	}
	if first.Header().Get("RateLimit-Remaining") != "1" || second.Header().Get("RateLimit-Remaining") != "0" {
		t.Fatalf("remaining headers = %q, %q", first.Header().Get("RateLimit-Remaining"), second.Header().Get("RateLimit-Remaining"))
	}
	if third.Header().Get("Retry-After") != "60" || !strings.Contains(third.Body.String(), `"code":"REQUEST_RATE_LIMITED"`) {
		t.Fatalf("rejection headers/body = %#v %s", third.Header(), third.Body.String())
	}
	if independent := serve("user:b", "192.0.2.10:443", "203.0.113.9"); independent.Code != http.StatusNoContent {
		t.Fatalf("independent principal status = %d", independent.Code)
	}
	if independent := serve("user:a", "192.0.2.11:443", "203.0.113.9"); independent.Code != http.StatusNoContent {
		t.Fatalf("independent direct IP status = %d", independent.Code)
	}
}

func TestAPIAdmissionCapsConcurrentSSEStreams(t *testing.T) {
	controller := NewAPIAdmissionController(APIAdmissionLimits{
		RequestsPerWindow: 10,
		Window:            time.Minute,
		SSEConcurrent:     1,
	}, time.Now)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	handler := controller.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))

	request := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations/op_1/events?stream=sse", nil)
		r.RemoteAddr = "192.0.2.20:443"
		ctx := idempotency.WithActor(r.Context(), "user:a")
		return r.WithContext(ctx)
	}
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request())
		firstDone <- response
	}()
	<-started

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, request())
	if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") != "1" {
		t.Fatalf("second stream = %d headers=%#v body=%s", second.Code, second.Header(), second.Body.String())
	}
	close(release)
	if first := <-firstDone; first.Code != http.StatusNoContent {
		t.Fatalf("first stream status = %d", first.Code)
	}

	third := httptest.NewRecorder()
	handler.ServeHTTP(third, request())
	if third.Code != http.StatusNoContent {
		t.Fatalf("stream after release status = %d body=%s", third.Code, third.Body.String())
	}
}

func TestRouterWiresAuthenticatedAdmission(t *testing.T) {
	router := NewRouterWithDataStack(DataStack{
		OperationStore: NewMemoryOperationStore(),
		APIAdmissionLimits: APIAdmissionLimits{
			RequestsPerWindow: 1,
			Window:            time.Minute,
			SSEConcurrent:     1,
		},
	})
	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations", nil))
	second := httptest.NewRecorder()
	router.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/api/v1alpha/operations", nil))
	if first.Code != http.StatusOK || second.Code != http.StatusTooManyRequests {
		t.Fatalf("router statuses = %d, %d; second body=%s", first.Code, second.Code, second.Body.String())
	}
}
