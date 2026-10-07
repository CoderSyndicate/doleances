// Package storage is the object store behind generated artefacts.
//
// It wraps gocloud.dev/blob so the backing service is a configuration choice:
// a directory in development, an S3-compatible bucket in production, with no
// code change between them. Nothing here is named after a vendor — S3 is one
// possible technology behind this interface, not the interface.
//
// Only file:// and s3:// are registered. Adding gcsblob pulls the whole Google
// Cloud client tree into an image that is meant to be distroless and minimal;
// the driver can be added the day somebody deploys on GCS, and that is a
// one-line change because everything here goes through blob.OpenBucket.
package storage

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gocloud.dev/blob"
	_ "gocloud.dev/blob/fileblob" // file:// — development and single-host deployments
	_ "gocloud.dev/blob/s3blob"   // s3:// — and every S3-compatible provider
)

// Store is the subset of object storage this project needs. It is an
// interface so a test does not need a bucket, and so the backing service
// stays swappable.
type Store interface {
	Write(ctx context.Context, key string, data []byte) error
	Read(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	Close() error
}

// bucketStore is the gocloud.dev implementation.
type bucketStore struct {
	bucket *blob.Bucket
}

// Open resolves a storage URL into a Store.
//
// A file:// URL has its directory created first: gocloud.dev's fileblob
// refuses to open a directory that does not exist, and failing at startup
// because nobody had made a folder is a poor first run.
func Open(ctx context.Context, rawURL string) (Store, error) {
	if strings.HasPrefix(rawURL, "file://") {
		if err := ensureDirectory(rawURL); err != nil {
			return nil, err
		}
	}

	bucket, err := blob.OpenBucket(ctx, rawURL)
	if err != nil {
		return nil, fmt.Errorf("storage: open %q: %w", rawURL, err)
	}
	return &bucketStore{bucket: bucket}, nil
}

func ensureDirectory(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("storage: parse %q: %w", rawURL, err)
	}

	// file://./storage puts "." in Host and "/storage" in Path; an absolute
	// file:///var/lib/x puts it all in Path. Join them back together.
	dir := filepath.Join(parsed.Host, filepath.FromSlash(parsed.Path))
	if dir == "" {
		return fmt.Errorf("storage: %q names no directory", rawURL)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("storage: create %s: %w", dir, err)
	}
	return nil
}

func (s *bucketStore) Write(ctx context.Context, key string, data []byte) error {
	w, err := s.bucket.NewWriter(ctx, key, nil)
	if err != nil {
		return fmt.Errorf("storage: write %s: %w", key, err)
	}
	if _, err := w.Write(data); err != nil {
		w.Close() //nolint:errcheck // the write error is the one worth reporting
		return fmt.Errorf("storage: write %s: %w", key, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("storage: close %s: %w", key, err)
	}
	return nil
}

func (s *bucketStore) Read(ctx context.Context, key string) ([]byte, error) {
	r, err := s.bucket.NewReader(ctx, key, nil)
	if err != nil {
		return nil, fmt.Errorf("storage: read %s: %w", key, err)
	}
	defer r.Close() //nolint:errcheck

	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("storage: read %s: %w", key, err)
	}
	return data, nil
}

func (s *bucketStore) Delete(ctx context.Context, key string) error {
	if err := s.bucket.Delete(ctx, key); err != nil {
		return fmt.Errorf("storage: delete %s: %w", key, err)
	}
	return nil
}

func (s *bucketStore) Close() error { return s.bucket.Close() }
