package backup

import (
	miniogo "github.com/minio/minio-go/v7"
)

// miniogoMakeOpts returns the MakeBucketOptions value the
// integration test uses. The helper lives in the non-integration
// file so the test build tag does not need to import the upstream
// package to access the type.
func miniogoMakeOpts() miniogo.MakeBucketOptions {
	return miniogo.MakeBucketOptions{Region: "us-east-1"}
}
