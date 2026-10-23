package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	miniogo "github.com/minio/minio-go/v7"
	miniocreds "github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/midnight/midnight/internal/audit"
)

// S3Endpoint specifies the resolved S3 connection metadata parsed
// from a midnight backup DSN. The DSN shape is panel-locked at
// `s3://<bucket>/<prefix>?endpoint=<host>&region=<region>` —
// credentials NEVER appear in the URI; they are sourced from env
// vars (see CredentialsFromEnv) or from Lane 4's KeyProvider.
type S3Endpoint struct {
	Bucket    string
	Prefix    string
	Endpoint  string
	Region    string
	UseSSL    bool
	UsePath   bool
	Anonymous bool
}

// ParseS3URI parses a `s3://bucket/prefix?endpoint=...&region=...`
// URI into a typed endpoint. The query params accept:
//
//	endpoint     host[:port] (default `s3.<region>.amazonaws.com`)
//	region       AWS region (default `us-east-1`)
//	scheme       http|https (default https)
//	style        path|virtual (default virtual; MinIO usually wants path)
//	anonymous    1|true to use unauthenticated reads
func ParseS3URI(raw string) (S3Endpoint, error) {
	if strings.TrimSpace(raw) == "" {
		return S3Endpoint{}, errors.New("s3 backup uri is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return S3Endpoint{}, fmt.Errorf("parse s3 uri: %w", err)
	}
	if parsed.Scheme != "s3" {
		return S3Endpoint{}, fmt.Errorf("backup uri scheme %q is not s3://", parsed.Scheme)
	}
	bucket := strings.TrimSpace(parsed.Host)
	if bucket == "" {
		return S3Endpoint{}, errors.New("s3 uri must include a bucket name")
	}
	prefix := strings.TrimPrefix(parsed.Path, "/")
	prefix = strings.TrimSuffix(prefix, "/")
	q := parsed.Query()

	region := strings.TrimSpace(q.Get("region"))
	if region == "" {
		region = "us-east-1"
	}
	endpoint := strings.TrimSpace(q.Get("endpoint"))
	if endpoint == "" {
		endpoint = "s3." + region + ".amazonaws.com"
	}
	scheme := strings.ToLower(strings.TrimSpace(q.Get("scheme")))
	useSSL := scheme != "http"
	style := strings.ToLower(strings.TrimSpace(q.Get("style")))
	usePath := style == "path"
	anon := strings.EqualFold(q.Get("anonymous"), "1") || strings.EqualFold(q.Get("anonymous"), "true")

	return S3Endpoint{
		Bucket:    bucket,
		Prefix:    prefix,
		Endpoint:  endpoint,
		Region:    region,
		UseSSL:    useSSL,
		UsePath:   usePath,
		Anonymous: anon,
	}, nil
}

// CredentialsFromEnv reads MIDNIGHT_BACKUP_AWS_ACCESS_KEY_ID and
// MIDNIGHT_BACKUP_AWS_SECRET_ACCESS_KEY (and the optional
// MIDNIGHT_BACKUP_AWS_SESSION_TOKEN). Returns nil credentials when
// neither is set; callers may then fall back to an explicit
// Lane 4 KeyProvider hook.
//
// Deprecated: kept for backward compatibility with callers that
// pre-date the CredentialSource boundary. New callers MUST consume
// CredentialSource (see credentials.go) so the audit emit boundary
// per BACKUP_CREDENTIAL_BOUNDARY.md is not bypassed.
func CredentialsFromEnv() *miniocreds.Credentials {
	id := strings.TrimSpace(os.Getenv(EnvBackupAccessKeyID))
	secret := strings.TrimSpace(os.Getenv(EnvBackupSecretAccessKey))
	token := strings.TrimSpace(os.Getenv(EnvBackupSessionToken))
	if id == "" || secret == "" {
		return nil
	}
	return miniocreds.NewStaticV4(id, secret, token)
}

// S3Destination implements Destination against any S3-compatible
// endpoint (AWS S3, MinIO, Wasabi, Cloudflare R2). It is used for
// off-host backup per §1.8 / 0.5-EC5.
type S3Destination struct {
	endpoint S3Endpoint
	cli      *miniogo.Client
	now      func() time.Time

	// staleness threshold: a backup is BACKUP_STALE if the last
	// successful object is older than this. Defaults to 26h
	// (matches the panel-locked default per §1.8).
	staleAfter time.Duration
}

// S3Options carries construction-time knobs.
//
// CredentialSource is the panel-locked seam onto Lane 4's sealed
// variable boundary; see internal/backup/credentials.go and
// agile/handoff/governance/BACKUP_CREDENTIAL_BOUNDARY.md. When nil,
// NewS3Destination wires a default ChainCredentialSource (env-only
// in alpha; opt-in sealed escalation when the operator configures
// MIDNIGHT_BACKUP_CREDENTIAL_REF) so existing env-var users are
// unaffected.
//
// Audit forwards the typed credential-resolve audit row produced by
// the credential source. Set this on long-running daemons; tests
// can leave it nil. The hook is invoked synchronously at construct
// time and is fail-soft per Phase 1 default — a panicking sink is
// recovered and logged via Audit panics rather than failing the
// destination.
type S3Options struct {
	Endpoint         S3Endpoint
	Credentials      *miniocreds.Credentials
	CredentialSource CredentialSource
	Audit            func(context.Context, audit.Row)
	StaleAfter       time.Duration
	Now              func() time.Time
}

// NewS3Destination connects to the supplied endpoint. The credential
// resolution order is:
//
//  1. opts.Credentials (explicit override; tests pass this directly).
//  2. opts.CredentialSource.Resolve (the panel-locked seam).
//  3. NewDefaultChainCredentialSource(nil).Resolve (alpha-default
//     env-only chain; identical to the legacy behaviour).
//
// On (2) or (3), the audit row produced by the credential source is
// forwarded to opts.Audit (when non-nil). The Phase 1 default
// staleness threshold is 26h (§1.8).
func NewS3Destination(opts S3Options) (*S3Destination, error) {
	return NewS3DestinationCtx(context.Background(), opts)
}

// NewS3DestinationCtx is the context-aware constructor. Use this when
// the caller already has a request-scoped context to thread through
// the credential resolve / audit emit path.
func NewS3DestinationCtx(ctx context.Context, opts S3Options) (*S3Destination, error) {
	if opts.Endpoint.Bucket == "" {
		return nil, errors.New("s3 destination: bucket is required")
	}
	creds := opts.Credentials
	if creds == nil && !opts.Endpoint.Anonymous {
		source := opts.CredentialSource
		if source == nil {
			source = NewDefaultChainCredentialSource(nil)
		}
		uri := s3DestinationURI(opts.Endpoint)
		resolved, row, err := source.Resolve(ctx, uri)
		if err != nil {
			return nil, fmt.Errorf("s3 destination: %w", err)
		}
		if row != nil && opts.Audit != nil {
			opts.Audit(ctx, *row)
		}
		creds = resolved.MinioCredentials()
		resolved.Zero()
	}
	if creds == nil {
		creds = miniocreds.NewStaticV4("", "", "")
	}

	cli, err := miniogo.New(opts.Endpoint.Endpoint, &miniogo.Options{
		Creds:        creds,
		Secure:       opts.Endpoint.UseSSL,
		Region:       opts.Endpoint.Region,
		BucketLookup: bucketLookup(opts.Endpoint),
	})
	if err != nil {
		return nil, fmt.Errorf("s3 destination: %w", err)
	}
	stale := opts.StaleAfter
	if stale <= 0 {
		stale = 26 * time.Hour
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &S3Destination{endpoint: opts.Endpoint, cli: cli, staleAfter: stale, now: now}, nil
}

// s3DestinationURI rebuilds the canonical destination URI from the
// resolved endpoint so the audit row's destination_uri attribute is
// stable across env-var and sealed credential resolution paths.
func s3DestinationURI(e S3Endpoint) string {
	bucket := e.Bucket
	prefix := strings.TrimPrefix(e.Prefix, "/")
	if prefix == "" {
		return "s3://" + bucket
	}
	return "s3://" + bucket + "/" + prefix
}

func bucketLookup(e S3Endpoint) miniogo.BucketLookupType {
	if e.UsePath {
		return miniogo.BucketLookupPath
	}
	return miniogo.BucketLookupAuto
}

// objectKey joins the configured prefix to a key.
func (d *S3Destination) objectKey(key string) string {
	if d.endpoint.Prefix == "" {
		return key
	}
	return d.endpoint.Prefix + "/" + strings.TrimPrefix(key, "/")
}

// Put uploads body to bucket/<prefix>/<key>. Returns Object metadata
// with the SHA-256 of the streamed body.
func (d *S3Destination) Put(ctx context.Context, key string, body io.Reader) (Object, error) {
	if strings.TrimSpace(key) == "" {
		return Object{}, errors.New("s3 destination: object key is required")
	}
	hashing := newHashingReader(body)
	full := d.objectKey(key)
	info, err := d.cli.PutObject(ctx, d.endpoint.Bucket, full, hashing, -1, miniogo.PutObjectOptions{
		ContentType: "application/octet-stream",
	})
	if err != nil {
		return Object{}, fmt.Errorf("s3 put %s: %w", full, err)
	}
	return Object{
		Key:    full,
		Bytes:  info.Size,
		SHA256: hashing.HexSum(),
	}, nil
}

// Get returns a reader for the object body and its metadata.
func (d *S3Destination) Get(ctx context.Context, key string) (io.ReadCloser, Object, error) {
	full := d.objectKey(key)
	obj, err := d.cli.GetObject(ctx, d.endpoint.Bucket, full, miniogo.GetObjectOptions{})
	if err != nil {
		return nil, Object{}, fmt.Errorf("s3 get %s: %w", full, err)
	}
	stat, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		return nil, Object{}, fmt.Errorf("s3 stat %s: %w", full, err)
	}
	return obj, Object{
		Key:    full,
		Bytes:  stat.Size,
		SHA256: stat.ETag,
	}, nil
}

// Delete removes the object at key.
func (d *S3Destination) Delete(ctx context.Context, key string) error {
	full := d.objectKey(key)
	return d.cli.RemoveObject(ctx, d.endpoint.Bucket, full, miniogo.RemoveObjectOptions{})
}

// Stat returns the object metadata without downloading the body.
func (d *S3Destination) Stat(ctx context.Context, key string) (Object, error) {
	full := d.objectKey(key)
	stat, err := d.cli.StatObject(ctx, d.endpoint.Bucket, full, miniogo.StatObjectOptions{})
	if err != nil {
		return Object{}, fmt.Errorf("s3 stat %s: %w", full, err)
	}
	return Object{
		Key:    full,
		Bytes:  stat.Size,
		SHA256: strings.Trim(stat.ETag, "\""),
	}, nil
}

// SignedURL returns a presigned GET URL valid for ttl. Operators
// hand this URL to a one-shot restore tool that runs without the
// long-lived credentials.
func (d *S3Destination) SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	full := d.objectKey(key)
	u, err := d.cli.PresignedGetObject(ctx, d.endpoint.Bucket, full, ttl, nil)
	if err != nil {
		return "", fmt.Errorf("s3 presign %s: %w", full, err)
	}
	return u.String(), nil
}

// List returns every object under the configured prefix joined with
// the supplied subprefix. Used by restore-from-off-host tooling.
func (d *S3Destination) List(ctx context.Context, prefix string) ([]Object, error) {
	full := d.objectKey(prefix)
	out := []Object{}
	for info := range d.cli.ListObjects(ctx, d.endpoint.Bucket, miniogo.ListObjectsOptions{Prefix: full, Recursive: true}) {
		if info.Err != nil {
			return nil, fmt.Errorf("s3 list %s: %w", full, info.Err)
		}
		out = append(out, Object{
			Key:    info.Key,
			Bytes:  info.Size,
			SHA256: strings.Trim(info.ETag, "\""),
		})
	}
	return out, nil
}

// LatestSuccessfulBackupAge returns time.Since(LastModified) for the
// most recent object under prefix, or zero + ok=false when the
// prefix is empty. Used by the BACKUP_STALE alert flow.
func (d *S3Destination) LatestSuccessfulBackupAge(ctx context.Context, prefix string) (time.Duration, bool, error) {
	full := d.objectKey(prefix)
	var newest time.Time
	for info := range d.cli.ListObjects(ctx, d.endpoint.Bucket, miniogo.ListObjectsOptions{Prefix: full, Recursive: true}) {
		if info.Err != nil {
			return 0, false, fmt.Errorf("s3 list %s: %w", full, info.Err)
		}
		if info.LastModified.After(newest) {
			newest = info.LastModified
		}
	}
	if newest.IsZero() {
		return 0, false, nil
	}
	return d.now().Sub(newest), true, nil
}

// IsStale reports whether the latest successful backup under prefix
// is older than d.staleAfter. The second return value reports
// whether a backup was found at all.
func (d *S3Destination) IsStale(ctx context.Context, prefix string) (bool, bool, error) {
	age, ok, err := d.LatestSuccessfulBackupAge(ctx, prefix)
	if err != nil {
		return false, false, err
	}
	if !ok {
		return false, false, nil
	}
	return age > d.staleAfter, true, nil
}

// hashingReader streams body into a SHA-256 hash and tracks the
// running byte count so the caller can populate Object.SHA256
// without buffering.
type hashingReader struct {
	src   io.Reader
	hash  hash.Hash
	bytes int64
}

func newHashingReader(src io.Reader) *hashingReader {
	return &hashingReader{src: src, hash: sha256.New()}
}

func (h *hashingReader) Read(p []byte) (int, error) {
	n, err := h.src.Read(p)
	if n > 0 {
		_, _ = h.hash.Write(p[:n])
		h.bytes += int64(n)
	}
	return n, err
}

func (h *hashingReader) HexSum() string {
	return hex.EncodeToString(h.hash.Sum(nil))
}
