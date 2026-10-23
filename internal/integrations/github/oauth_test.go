package github

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type setupHTTPDoer func(*http.Request) (*http.Response, error)

func (do setupHTTPDoer) Do(request *http.Request) (*http.Response, error) {
	return do(request)
}

func setupHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestAppClientOAuthProofNeverTrustsCallbackOwnership(t *testing.T) {
	_, key := mustGenTestKey(t)
	cfg := AppConfig{
		AppID: 7, PrivateKeyPEM: key,
		ClientID: "Iv1.client", ClientSecret: "client-secret",
		AppSlug: "midnight-app", CallbackURL: "https://midnight.example/api/v1alpha/github/setup/callback",
		APIBaseURL: "https://api.github.example", WebBaseURL: "https://github.example",
	}
	var calls int
	client, err := NewAppClient(cfg, WithHTTPDoer(setupHTTPDoer(func(request *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			if request.Method != http.MethodPost || request.URL.String() != "https://github.example/login/oauth/access_token" {
				t.Fatalf("OAuth request = %s %s", request.Method, request.URL)
			}
			body, _ := io.ReadAll(request.Body)
			if !strings.Contains(string(body), "code=one-time-code") || !strings.Contains(string(body), "client_secret=client-secret") {
				t.Fatalf("OAuth form = %q", body)
			}
			return setupHTTPResponse(http.StatusOK, `{"access_token":"ghu_user_proof"}`), nil
		case 2:
			if request.URL.String() != "https://api.github.example/user/installations/42/repositories?per_page=1" {
				t.Fatalf("installation proof URL = %s", request.URL)
			}
			if request.Header.Get("Authorization") != "Bearer ghu_user_proof" {
				t.Fatal("installation proof did not use the exchanged user token")
			}
			return setupHTTPResponse(http.StatusOK, `{"total_count":0,"repositories":[]}`), nil
		default:
			t.Fatalf("unexpected HTTP call %d", calls)
			return nil, nil
		}
	})))
	if err != nil {
		t.Fatal(err)
	}
	token, err := client.ExchangeUserCode(context.Background(), "one-time-code")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.UserCanAccessInstallation(context.Background(), token, 42); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("HTTP calls = %d", calls)
	}
}

func TestOAuthExchangeRedactsCodeAndClientSecret(t *testing.T) {
	_, key := mustGenTestKey(t)
	cfg := AppConfig{
		AppID: 7, PrivateKeyPEM: key,
		ClientID: "Iv1.client", ClientSecret: "client-secret",
		AppSlug: "midnight-app", CallbackURL: "https://midnight.example/api/v1alpha/github/setup/callback",
		WebBaseURL: "https://github.example",
	}
	client, err := NewAppClient(cfg, WithHTTPDoer(setupHTTPDoer(func(*http.Request) (*http.Response, error) {
		return setupHTTPResponse(http.StatusBadRequest, `{"error":"client-secret rejected one-time-code"}`), nil
	})))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ExchangeUserCode(context.Background(), "one-time-code")
	if err == nil || strings.Contains(err.Error(), "client-secret") || strings.Contains(err.Error(), "one-time-code") {
		t.Fatalf("redacted exchange error = %v", err)
	}
}
