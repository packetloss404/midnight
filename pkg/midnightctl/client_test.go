package midnightctl

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientBearerAuthAndRepresentativeResourceRoutes(t *testing.T) {
	t.Parallel()

	type expectedRequest struct {
		path     string
		rawQuery string
	}
	expected := []expectedRequest{
		{path: "/api/v1alpha/projects"},
		{path: "/api/v1alpha/projects/prj_alpha"},
		{path: "/api/v1alpha/services", rawQuery: "project_id=prj_alpha"},
		{path: "/api/v1alpha/services/svc_api"},
		{path: "/api/v1alpha/operations/op_deploy"},
	}
	requestIndex := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if requestIndex >= len(expected) {
			t.Errorf("unexpected request %s %s", request.Method, request.URL.String())
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		want := expected[requestIndex]
		requestIndex++
		if request.Method != http.MethodGet || request.URL.Path != want.path || request.URL.RawQuery != want.rawQuery {
			t.Errorf("request = %s %s?%s, want GET %s?%s", request.Method, request.URL.Path, request.URL.RawQuery, want.path, want.rawQuery)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer midn_test_token" {
			t.Errorf("Authorization = %q, want bearer token", got)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL+"/api/v1alpha", WithBearerToken("midn_test_token"))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	projectID := ServiceProjectFilter("prj_alpha")
	requests := []func(context.Context) (*http.Response, error){
		func(ctx context.Context) (*http.Response, error) { return client.ListProjects(ctx) },
		func(ctx context.Context) (*http.Response, error) { return client.GetProject(ctx, "prj_alpha") },
		func(ctx context.Context) (*http.Response, error) {
			return client.ListServices(ctx, &ListServicesParams{ProjectId: &projectID})
		},
		func(ctx context.Context) (*http.Response, error) { return client.GetService(ctx, "svc_api") },
		func(ctx context.Context) (*http.Response, error) { return client.GetOperation(ctx, "op_deploy") },
	}
	for index, send := range requests {
		response, err := send(context.Background())
		if err != nil {
			t.Fatalf("request %d error = %v", index, err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("request %d status = %d", index, response.StatusCode)
		}
	}
	if requestIndex != len(expected) {
		t.Fatalf("received %d requests, want %d", requestIndex, len(expected))
	}
}

func TestWithBearerTokenRejectsUnsafeValues(t *testing.T) {
	t.Parallel()

	for _, token := range []string{"", "   ", " token", "token ", "token\r\nInjected: value"} {
		t.Run(fmt.Sprintf("%q", token), func(t *testing.T) {
			if _, err := NewClient("https://api.midnight.invalid", WithBearerToken(token)); err == nil {
				t.Fatalf("NewClient() accepted unsafe token %q", token)
			}
		})
	}
}
