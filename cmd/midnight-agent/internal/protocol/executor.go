package protocol

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Doer is the subset of http.Client used by the agent protocol client.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Executor executes prebuilt protocol requests.
type Executor interface {
	Execute(*http.Request, any) (ResponseMeta, error)
	ExecuteDesiredState(*http.Request) (DesiredStateResponse, error)
}

// ResponseMeta contains HTTP response metadata callers may need to retain.
type ResponseMeta struct {
	StatusCode int
	ETag       string
}

// Client executes agent protocol requests through an injected Doer.
type Client struct {
	Doer Doer
}

// Execute sends a prebuilt request and JSON-decodes a successful response into out.
func (c Client) Execute(req *http.Request, out any) (ResponseMeta, error) {
	resp, err := c.do(req)
	if err != nil {
		return ResponseMeta{}, err
	}
	defer resp.Body.Close()

	meta := responseMeta(resp)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return meta, decodeHTTPError(resp)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return meta, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return meta, fmt.Errorf("decode response body: %w", err)
	}
	return meta, nil
}

// ExecuteDesiredState sends a desired-state request and preserves the response ETag.
func (c Client) ExecuteDesiredState(req *http.Request) (DesiredStateResponse, error) {
	resp, err := c.do(req)
	if err != nil {
		return DesiredStateResponse{}, err
	}
	defer resp.Body.Close()

	result := DesiredStateResponse{ETag: strings.TrimSpace(resp.Header.Get("ETag"))}
	if resp.StatusCode == http.StatusNotModified {
		result.NotModified = true
		_, _ = io.Copy(io.Discard, resp.Body)
		return result, nil
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return result, decodeHTTPError(resp)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return result, fmt.Errorf("read desired-state body: %w", err)
	}
	result.Raw = body
	return result, nil
}

func (c Client) do(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("request is required")
	}
	if c.Doer == nil {
		return nil, fmt.Errorf("doer is required")
	}
	resp, err := c.Doer.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	if resp == nil {
		return nil, fmt.Errorf("execute request: nil response")
	}
	if resp.Body == nil {
		resp.Body = io.NopCloser(strings.NewReader(""))
	}
	return resp, nil
}

func responseMeta(resp *http.Response) ResponseMeta {
	return ResponseMeta{StatusCode: resp.StatusCode, ETag: strings.TrimSpace(resp.Header.Get("ETag"))}
}
