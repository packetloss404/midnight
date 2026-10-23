package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/strictjson"
)

func TestDecodeStrictJSONRequestRejectsUnknownTrailingAndOversizeBodies(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "valid", body: `{"name":"midnight"}`, wantStatus: http.StatusNoContent},
		{name: "unknown", body: `{"name":"midnight","extra":true}`, wantStatus: http.StatusBadRequest},
		{name: "duplicate", body: `{"name":"first","name":"second"}`, wantStatus: http.StatusBadRequest},
		{name: "trailing", body: `{"name":"midnight"} {}`, wantStatus: http.StatusBadRequest},
		{name: "oversize", body: `{"name":"` + strings.Repeat("x", int(defaultJSONBodyLimitBytes)) + `"}`, wantStatus: http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var value payload
				if !decodeStrictJSONRequest(w, r, &value, "invalid request") {
					return
				}
				w.WriteHeader(http.StatusNoContent)
			})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body)))
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d body=%s, want %d", response.Code, response.Body.String(), test.wantStatus)
			}
		})
	}
}

func TestRejectDuplicateJSONKeysRejectsNestedDuplicates(t *testing.T) {
	var target any
	if err := strictjson.Decode([]byte(`{"outer":{"value":1,"value":2}}`), &target); err == nil {
		t.Fatal("nested duplicate accepted")
	}
	if err := strictjson.Decode([]byte(`{"outer":{"value":1},"items":[{"value":2}]}`), &target); err != nil {
		t.Fatalf("valid nested document rejected: %v", err)
	}
}

func TestReadBoundedJSONRequestBodyPreservesExactOctets(t *testing.T) {
	const raw = " {\n  \"name\": \"midnight\"\n}\n"
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(raw))
	response := httptest.NewRecorder()
	body, ok := readBoundedJSONRequestBody(response, request, "invalid request")
	if !ok {
		t.Fatalf("body rejected: status=%d body=%s", response.Code, response.Body.String())
	}
	if string(body) != raw {
		t.Fatalf("body = %q, want exact %q", body, raw)
	}
	var value struct {
		Name string `json:"name"`
	}
	if !decodeStrictJSONRequestBytes(response, request, body, &value, "invalid request") {
		t.Fatalf("strict decode rejected preserved body: status=%d body=%s", response.Code, response.Body.String())
	}
	if value.Name != "midnight" {
		t.Fatalf("name = %q, want midnight", value.Name)
	}
}

func TestDecodeStrictJSONRequestBodyWithLimitRejectsOversize(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"too long"}`))
	response := httptest.NewRecorder()
	var value struct {
		Name string `json:"name"`
	}
	if _, ok := decodeStrictJSONRequestBodyWithLimit(response, request, &value, "invalid request", 8); ok {
		t.Fatal("oversized route-specific JSON body accepted")
	}
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d body=%s, want %d", response.Code, response.Body.String(), http.StatusRequestEntityTooLarge)
	}
}

func TestDecodeOptionalStrictJSONRequestBytesAllowsOnlyEmptyOrOneDocument(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	response := httptest.NewRecorder()
	var value struct{}
	if !decodeOptionalStrictJSONRequestBytes(response, request, nil, &value, "invalid request") {
		t.Fatal("empty optional body rejected")
	}

	response = httptest.NewRecorder()
	if decodeOptionalStrictJSONRequestBytes(response, request, []byte(`{} {}`), &value, "invalid request") {
		t.Fatal("multiple JSON documents accepted")
	}
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}
