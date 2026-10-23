package midnightctl

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

var errInvalidBearerToken = errors.New("midnightctl: bearer token must be non-empty, trimmed, and contain no line breaks")

// WithBearerToken adds an Authorization header to every request made by the
// generated client. Human sessions, project automation tokens, and recovery
// credentials all use the same HTTP bearer scheme; server-side authorization
// still determines which operations each credential may perform.
func WithBearerToken(token string) ClientOption {
	return func(client *Client) error {
		if strings.TrimSpace(token) == "" || strings.TrimSpace(token) != token || strings.ContainsAny(token, "\r\n") {
			return errInvalidBearerToken
		}
		client.RequestEditors = append(client.RequestEditors, func(_ context.Context, request *http.Request) error {
			request.Header.Set("Authorization", "Bearer "+token)
			return nil
		})
		return nil
	}
}
