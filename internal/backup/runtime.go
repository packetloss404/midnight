package backup

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// OpenDestination resolves a configured backup URI into a concrete destination.
// It only returns implementations that perform real storage I/O.
func OpenDestination(ctx context.Context, rawURI string) (Destination, error) {
	rawURI = strings.TrimSpace(rawURI)
	if rawURI == "" {
		return nil, fmt.Errorf("%s is not configured", EnvBackupDestinationURI)
	}
	parsed, err := url.Parse(rawURI)
	if err != nil {
		return nil, fmt.Errorf("backup destination uri is invalid: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "local", "file":
		path, err := ParseLocalURI(rawURI)
		if err != nil {
			return nil, err
		}
		return NewLocalDestination(path)
	case "s3":
		endpoint, err := ParseS3URI(rawURI)
		if err != nil {
			return nil, err
		}
		return NewS3DestinationCtx(ctx, S3Options{Endpoint: endpoint})
	default:
		return nil, fmt.Errorf("backup destination scheme %q is not supported", parsed.Scheme)
	}
}
