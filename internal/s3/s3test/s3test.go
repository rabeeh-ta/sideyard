// Package s3test provides an in-memory S3 server for tests.
package s3test

import (
	"net/http/httptest"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"sideyard/internal/s3"
)

// FakeServer starts an in-memory S3 server with the given buckets, for
// tests. It returns a Config pointing at it and a function to stop it.
func FakeServer(buckets ...string) (s3.Config, func()) {
	backend := s3mem.New()
	for _, b := range buckets {
		backend.CreateBucket(b)
	}
	srv := httptest.NewServer(gofakes3.New(backend).Server())
	return s3.Config{
		Endpoint:  srv.URL,
		Region:    "us-east-1",
		Bucket:    buckets[0],
		AccessKey: "test",
		SecretKey: "test-secret",
	}, srv.Close
}
