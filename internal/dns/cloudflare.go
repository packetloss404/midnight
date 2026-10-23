// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultCloudflareBaseURL     = "https://api.cloudflare.com/client/v4"
	defaultCloudflareTimeout     = 10 * time.Second
	defaultCloudflareMaxAttempts = 3
	cloudflareResponseLimit      = 4 << 20
	cloudflarePageSize           = 100
)

type CloudflareConfig struct {
	Token             string
	BaseURL           string
	HTTPClient        *http.Client
	Timeout           time.Duration
	MaxAttempts       int
	AllowInsecureHTTP bool
	Backoff           func(int) time.Duration
	Sleep             func(context.Context, time.Duration) error
}

type CloudflareProvider struct {
	token       string
	baseURL     *url.URL
	client      *http.Client
	maxAttempts int
	backoff     func(int) time.Duration
	sleep       func(context.Context, time.Duration) error
}

func NewCloudflareProvider(config CloudflareConfig) (*CloudflareProvider, error) {
	token := strings.TrimSpace(config.Token)
	if token == "" {
		return nil, errors.New("Cloudflare API token is required")
	}
	base := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if base == "" {
		base = defaultCloudflareBaseURL
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !(config.AllowInsecureHTTP && parsed.Scheme == "http")) {
		return nil, errors.New("Cloudflare API base URL must be HTTPS")
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultCloudflareTimeout
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	} else if client.Timeout == 0 {
		copy := *client
		copy.Timeout = timeout
		client = &copy
	}
	attempts := config.MaxAttempts
	if attempts <= 0 {
		attempts = defaultCloudflareMaxAttempts
	}
	if attempts > 8 {
		return nil, errors.New("Cloudflare max attempts must not exceed 8")
	}
	backoff := config.Backoff
	if backoff == nil {
		backoff = func(attempt int) time.Duration {
			delay := time.Duration(1<<min(attempt, 5)) * 100 * time.Millisecond
			return min(delay, 5*time.Second)
		}
	}
	sleep := config.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	return &CloudflareProvider{token: token, baseURL: parsed, client: client, maxAttempts: attempts, backoff: backoff, sleep: sleep}, nil
}

func (*CloudflareProvider) Name() string { return "cloudflare" }

func (p *CloudflareProvider) ListRecords(ctx context.Context, zoneID string) ([]Record, error) {
	zoneID, err := validateCloudflareIdentifier(zoneID, "zone")
	if err != nil {
		return nil, err
	}
	out := []Record{}
	for page := 1; ; page++ {
		query := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(cloudflarePageSize)}}
		var response cloudflareRecordEnvelope
		if err := p.do(ctx, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/dns_records?"+query.Encode(), nil, &response); err != nil {
			return nil, err
		}
		for _, record := range response.Result {
			if !record.Type.valid() && record.Type != TypeNS {
				continue
			}
			out = append(out, Record{ID: record.ID, ZoneID: zoneID, Type: RecordType(record.Type), Name: record.Name, Content: record.Content, TTL: record.TTL})
		}
		if response.ResultInfo.TotalPages > 0 {
			if page >= response.ResultInfo.TotalPages {
				break
			}
		} else if len(response.Result) < cloudflarePageSize {
			break
		}
		if page >= 10000 {
			return nil, errors.New("Cloudflare DNS pagination exceeded its safety limit")
		}
	}
	return out, nil
}

func (p *CloudflareProvider) CreateRecord(ctx context.Context, zoneID string, record DesiredRecord) (Record, error) {
	zoneID, err := validateCloudflareIdentifier(zoneID, "zone")
	if err != nil {
		return Record{}, err
	}
	record, err = normalizeDesired(record)
	if err != nil {
		return Record{}, err
	}
	var response cloudflareSingleRecordEnvelope
	if err := p.do(ctx, http.MethodPost, "/zones/"+url.PathEscape(zoneID)+"/dns_records", cloudflareRecordRequest{Type: record.Type, Name: record.Name, Content: record.Content, TTL: record.TTL}, &response); err != nil {
		return Record{}, err
	}
	return cloudflareRecordToRecord(zoneID, response.Result)
}

func (p *CloudflareProvider) UpdateRecord(ctx context.Context, zoneID, recordID string, record DesiredRecord) (Record, error) {
	zoneID, err := validateCloudflareIdentifier(zoneID, "zone")
	if err != nil {
		return Record{}, err
	}
	recordID, err = validateCloudflareIdentifier(recordID, "record")
	if err != nil {
		return Record{}, err
	}
	record, err = normalizeDesired(record)
	if err != nil {
		return Record{}, err
	}
	var response cloudflareSingleRecordEnvelope
	path := "/zones/" + url.PathEscape(zoneID) + "/dns_records/" + url.PathEscape(recordID)
	// PATCH preserves provider-specific metadata such as Cloudflare proxy mode,
	// comments, and tags that are outside Midnight's provider-neutral contract.
	if err := p.do(ctx, http.MethodPatch, path, cloudflareRecordRequest{Type: record.Type, Name: record.Name, Content: record.Content, TTL: record.TTL}, &response); err != nil {
		return Record{}, err
	}
	return cloudflareRecordToRecord(zoneID, response.Result)
}

func (p *CloudflareProvider) DeleteRecord(ctx context.Context, zoneID, recordID string) error {
	zoneID, err := validateCloudflareIdentifier(zoneID, "zone")
	if err != nil {
		return err
	}
	recordID, err = validateCloudflareIdentifier(recordID, "record")
	if err != nil {
		return err
	}
	path := "/zones/" + url.PathEscape(zoneID) + "/dns_records/" + url.PathEscape(recordID)
	var response cloudflareDeleteEnvelope
	return p.do(ctx, http.MethodDelete, path, nil, &response)
}

type cloudflareRecord struct {
	ID      string     `json:"id"`
	Type    RecordType `json:"type"`
	Name    string     `json:"name"`
	Content string     `json:"content"`
	TTL     int        `json:"ttl"`
}

type cloudflareResultInfo struct {
	Page       int `json:"page"`
	TotalPages int `json:"total_pages"`
}

type cloudflareRecordEnvelope struct {
	Success    bool                 `json:"success"`
	Result     []cloudflareRecord   `json:"result"`
	Errors     []cloudflareAPIError `json:"errors"`
	ResultInfo cloudflareResultInfo `json:"result_info"`
}

type cloudflareSingleRecordEnvelope struct {
	Success bool                 `json:"success"`
	Result  cloudflareRecord     `json:"result"`
	Errors  []cloudflareAPIError `json:"errors"`
}

type cloudflareDeleteEnvelope struct {
	Success bool                 `json:"success"`
	Errors  []cloudflareAPIError `json:"errors"`
}

type cloudflareAPIError struct {
	Code int `json:"code"`
}

type cloudflareRecordRequest struct {
	Type    RecordType `json:"type"`
	Name    string     `json:"name"`
	Content string     `json:"content"`
	TTL     int        `json:"ttl"`
}

func (p *CloudflareProvider) do(ctx context.Context, method, path string, body any, target any) error {
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return errors.New("encode Cloudflare DNS request")
		}
	}
	for attempt := 0; attempt < p.maxAttempts; attempt++ {
		request, requestErr := http.NewRequestWithContext(ctx, method, p.baseURL.String()+path, bytes.NewReader(encoded))
		if requestErr != nil {
			return errors.New("build Cloudflare DNS request")
		}
		request.Header.Set("Authorization", "Bearer "+p.token)
		request.Header.Set("Accept", "application/json")
		request.Header.Set("User-Agent", "midnight-dns-reconciler/1")
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		response, requestErr := p.client.Do(request)
		if requestErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if attempt+1 < p.maxAttempts {
				if err := p.sleep(ctx, boundedCloudflareRetry(p.backoff(attempt), "")); err != nil {
					return err
				}
				continue
			}
			return errors.New("Cloudflare DNS request failed after retries")
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, cloudflareResponseLimit+1))
		_ = response.Body.Close()
		if readErr != nil {
			return errors.New("read Cloudflare DNS response")
		}
		if len(responseBody) > cloudflareResponseLimit {
			return errors.New("Cloudflare DNS response exceeds size limit")
		}
		if response.StatusCode == http.StatusNotFound {
			return ErrNotFound
		}
		if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
			if attempt+1 < p.maxAttempts {
				if err := p.sleep(ctx, boundedCloudflareRetry(p.backoff(attempt), response.Header.Get("Retry-After"))); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("Cloudflare DNS request failed after retries (status %d)", response.StatusCode)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("Cloudflare DNS request rejected (status %d)", response.StatusCode)
		}
		if target == nil {
			return nil
		}
		if err := json.Unmarshal(responseBody, target); err != nil {
			return errors.New("decode Cloudflare DNS response")
		}
		success, codes := cloudflareEnvelopeStatus(target)
		if !success {
			if len(codes) == 0 {
				return errors.New("Cloudflare DNS API reported failure")
			}
			return fmt.Errorf("Cloudflare DNS API reported error codes %s", strings.Join(codes, ","))
		}
		return nil
	}
	return errors.New("Cloudflare DNS request failed")
}

func cloudflareEnvelopeStatus(value any) (bool, []string) {
	var success bool
	var apiErrors []cloudflareAPIError
	switch envelope := value.(type) {
	case *cloudflareRecordEnvelope:
		success, apiErrors = envelope.Success, envelope.Errors
	case *cloudflareSingleRecordEnvelope:
		success, apiErrors = envelope.Success, envelope.Errors
	case *cloudflareDeleteEnvelope:
		success, apiErrors = envelope.Success, envelope.Errors
	default:
		return false, nil
	}
	codes := make([]string, 0, len(apiErrors))
	for _, apiError := range apiErrors {
		codes = append(codes, strconv.Itoa(apiError.Code))
	}
	return success, codes
}

func cloudflareRecordToRecord(zoneID string, raw cloudflareRecord) (Record, error) {
	return normalizeObserved(Record{ID: raw.ID, ZoneID: zoneID, Type: raw.Type, Name: raw.Name, Content: raw.Content, TTL: raw.TTL}, zoneID)
}

func validateCloudflareIdentifier(value, kind string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 32 || strings.ContainsAny(value, "/?#") {
		return "", fmt.Errorf("invalid Cloudflare %s id", kind)
	}
	return value, nil
}

func boundedCloudflareRetry(fallback time.Duration, retryAfter string) time.Duration {
	delay := fallback
	if seconds, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && seconds >= 0 {
		delay = time.Duration(seconds) * time.Second
	}
	if delay < 0 {
		delay = 0
	}
	return min(delay, 5*time.Minute)
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
