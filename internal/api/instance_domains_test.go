package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type memoryInstanceDomainSettingsStore struct {
	value InstanceDomainSettings
	set   bool
}

func (s *memoryInstanceDomainSettingsStore) Get(context.Context) (InstanceDomainSettings, error) {
	if !s.set {
		return InstanceDomainSettings{}, ErrInstanceDomainsNotConfigured
	}
	return s.value, nil
}

func (s *memoryInstanceDomainSettingsStore) Put(_ context.Context, value InstanceDomainSettings, expectedVersion int64) (InstanceDomainSettings, error) {
	if (!s.set && expectedVersion != 0) || (s.set && s.value.Version != expectedVersion) {
		return InstanceDomainSettings{}, ErrInstanceDomainsConflict
	}
	value.Version = expectedVersion + 1
	value.UpdatedAt = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	s.value, s.set = value, true
	return value, nil
}

func TestInstanceDomainsHandlerConfiguresDerivedHostnames(t *testing.T) {
	store := &memoryInstanceDomainSettingsStore{}
	body := bytes.NewBufferString(`{"root_domain":"Example.Com","ingress_target":"203.0.113.10","expected_version":0}`)
	req := httptest.NewRequest(http.MethodPut, instanceDomainsPath, body)
	req.Header.Set("If-Match", `"0"`)
	rec := httptest.NewRecorder()

	instanceDomainsHandler(store, "apps.example.com", nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Configured      bool   `json:"configured"`
		RootDomain      string `json:"root_domain"`
		ConsoleHostname string `json:"console_hostname"`
		AppsDomain      string `json:"apps_domain"`
		IngressTarget   string `json:"ingress_target"`
		RestartRequired bool   `json:"restart_required"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Configured || response.RootDomain != "example.com" ||
		response.ConsoleHostname != "midnight.example.com" ||
		response.AppsDomain != "apps.example.com" ||
		response.IngressTarget != "203.0.113.10" || response.RestartRequired {
		t.Fatalf("response = %#v", response)
	}
}

func TestInstanceDomainsHandlerReportsUnconfigured(t *testing.T) {
	store := &memoryInstanceDomainSettingsStore{}
	req := httptest.NewRequest(http.MethodGet, instanceDomainsPath, nil)
	rec := httptest.NewRecorder()

	instanceDomainsHandler(store, "", nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"0"` || rec.Body.String() != "{\"active_apps_domain\":\"\",\"configured\":false}\n" {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestInstanceDomainsHandlerRejectsStaleVersion(t *testing.T) {
	store := &memoryInstanceDomainSettingsStore{
		set: true,
		value: InstanceDomainSettings{
			RootDomain: "example.com",
			Version:    2,
		},
	}
	body := bytes.NewBufferString(`{"root_domain":"example.com","ingress_target":"192.0.2.10","expected_version":1}`)
	req := httptest.NewRequest(http.MethodPut, instanceDomainsPath, body)
	req.Header.Set("If-Match", `"1"`)
	rec := httptest.NewRecorder()

	instanceDomainsHandler(store, "apps.example.com", nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error.Code != "PRECONDITION_FAILED" {
		t.Fatalf("error code = %q", response.Error.Code)
	}
}

func TestNormalizeIngressTargetRejectsEmpty(t *testing.T) {
	if _, err := normalizeIngressTarget(" "); err == nil {
		t.Fatal("empty target accepted")
	}
}
