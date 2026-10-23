//go:build integration

package backup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	miniocreds "github.com/minio/minio-go/v7/pkg/credentials"
	miniotc "github.com/testcontainers/testcontainers-go/modules/minio"
)

// TestS3DestinationAgainstMinIO spins up a real MinIO testcontainer
// and exercises Put → Stat → Get → SignedURL → Delete plus the
// staleness probe. Default `go test ./...` skips this file because
// the build tag is `integration`.
//
// The test also asserts the restore-from-off-host smoke contract
// referenced in the §1.8 brief: an object PUT then re-GET'd must
// round-trip byte-exact.
func TestS3DestinationAgainstMinIO(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	const accessKey = "midnight-test"
	const secretKey = "midnight-test-secret"
	const bucket = "midnight-backups"

	mc, err := miniotc.Run(ctx, "minio/minio:RELEASE.2025-04-08T15-41-24Z",
		miniotc.WithUsername(accessKey),
		miniotc.WithPassword(secretKey),
	)
	if err != nil {
		t.Fatalf("start minio: %v", err)
	}
	t.Cleanup(func() {
		_ = mc.Terminate(context.Background())
	})

	endpoint, err := mc.Endpoint(ctx, "")
	if err != nil {
		t.Fatalf("endpoint: %v", err)
	}

	dst, err := NewS3Destination(S3Options{
		Endpoint: S3Endpoint{
			Bucket:   bucket,
			Prefix:   "smoke",
			Endpoint: endpoint,
			Region:   "us-east-1",
			UseSSL:   false,
			UsePath:  true,
		},
		Credentials: miniocreds.NewStaticV4(accessKey, secretKey, ""),
	})
	if err != nil {
		t.Fatalf("NewS3Destination: %v", err)
	}

	// MinIO defaults to no preexisting bucket; create it.
	makeBucketCtx, makeCancel := context.WithTimeout(ctx, 30*time.Second)
	defer makeCancel()
	if err := dst.cli.MakeBucket(makeBucketCtx, bucket, miniogoMakeOpts()); err != nil {
		t.Fatalf("MakeBucket: %v", err)
	}

	body := []byte("midnight-backup-restore-smoke-payload")
	obj, err := dst.Put(ctx, "demo/object.bin", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !strings.HasSuffix(obj.Key, "demo/object.bin") {
		t.Fatalf("Object.Key = %q", obj.Key)
	}
	if obj.Bytes != int64(len(body)) {
		t.Fatalf("Object.Bytes = %d, want %d", obj.Bytes, len(body))
	}

	got, getMeta, err := dst.Get(ctx, "demo/object.bin")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer got.Close()
	gotBytes, err := io.ReadAll(got)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(gotBytes, body) {
		t.Fatalf("round-trip mismatch: got %d bytes, want %d", len(gotBytes), len(body))
	}
	if getMeta.Bytes != int64(len(body)) {
		t.Fatalf("get meta Bytes = %d", getMeta.Bytes)
	}

	url, err := dst.SignedURL(ctx, "demo/object.bin", 5*time.Minute)
	if err != nil {
		t.Fatalf("SignedURL: %v", err)
	}
	if !strings.HasPrefix(url, "http") {
		t.Fatalf("SignedURL prefix = %q", url)
	}

	stale, found, err := dst.IsStale(ctx, "demo/")
	if err != nil {
		t.Fatalf("IsStale: %v", err)
	}
	if !found {
		t.Fatal("IsStale found=false; expected the just-PUT object to be discoverable")
	}
	if stale {
		t.Fatal("IsStale = true; expected freshly written object to NOT be stale")
	}

	if err := dst.Delete(ctx, "demo/object.bin"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := dst.Stat(ctx, "demo/object.bin"); err == nil {
		t.Fatal("Stat after Delete returned nil; expected NoSuchKey error")
	}
	t.Logf("S3 backup destination round-trip succeeded against MinIO at %s", endpoint)
	_ = fmt.Sprintf // keep the formatter import alive in case the helper grows
}
