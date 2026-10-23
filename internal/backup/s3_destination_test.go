package backup

import (
	"errors"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/failurecodes"
)

func TestParseS3URI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		uri     string
		want    S3Endpoint
		wantErr bool
	}{
		{
			name: "default region and endpoint",
			uri:  "s3://midnight-backups/project-foo",
			want: S3Endpoint{
				Bucket:   "midnight-backups",
				Prefix:   "project-foo",
				Endpoint: "s3.us-east-1.amazonaws.com",
				Region:   "us-east-1",
				UseSSL:   true,
			},
		},
		{
			name: "explicit endpoint and region",
			uri:  "s3://my-bucket/proj?endpoint=minio.example.local:9000&region=eu-west-1&style=path&scheme=http",
			want: S3Endpoint{
				Bucket:   "my-bucket",
				Prefix:   "proj",
				Endpoint: "minio.example.local:9000",
				Region:   "eu-west-1",
				UseSSL:   false,
				UsePath:  true,
			},
		},
		{
			name: "anonymous flag",
			uri:  "s3://public-bucket/?anonymous=1",
			want: S3Endpoint{
				Bucket:    "public-bucket",
				Prefix:    "",
				Endpoint:  "s3.us-east-1.amazonaws.com",
				Region:    "us-east-1",
				UseSSL:    true,
				Anonymous: true,
			},
		},
		{name: "missing bucket", uri: "s3:///prefix", wantErr: true},
		{name: "wrong scheme", uri: "https://bucket/prefix", wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseS3URI(tt.uri)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseS3URI(%q) error = nil, want error", tt.uri)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseS3URI(%q) error = %v", tt.uri, err)
			}
			if got != tt.want {
				t.Fatalf("ParseS3URI(%q) = %+v, want %+v", tt.uri, got, tt.want)
			}
		})
	}
}

func TestNewS3DestinationRefusesMissingCreds(t *testing.T) {
	t.Parallel()

	emptyEnv := func(string) (string, bool) { return "", false }
	source := &ChainCredentialSource{
		Env:       &EnvCredentialSource{LookupEnv: emptyEnv},
		LookupEnv: emptyEnv,
	}
	_, err := NewS3Destination(S3Options{
		Endpoint:         S3Endpoint{Bucket: "x"},
		CredentialSource: source,
	})
	if err == nil {
		t.Fatal("NewS3Destination expected error for missing credentials")
	}
	var failure *failurecodes.FailureError
	if !errors.As(err, &failure) {
		t.Fatalf("expected *failurecodes.FailureError, got %T: %v", err, err)
	}
	if failure.Code != failurecodes.BackupCredentialsMissing {
		t.Fatalf("failure.Code = %s, want BACKUP_CREDENTIALS_MISSING", failure.Code)
	}
	if !strings.Contains(err.Error(), "BACKUP_CREDENTIALS_MISSING") {
		t.Fatalf("error = %q, want code in message", err.Error())
	}
}

func TestHashingReaderHexSum(t *testing.T) {
	t.Parallel()

	hr := newHashingReader(strings.NewReader("hello world"))
	buf := make([]byte, 64)
	if _, err := hr.Read(buf); err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if got := hr.HexSum(); got != want {
		t.Fatalf("HexSum = %q, want %q", got, want)
	}
}
