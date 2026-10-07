package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// snapshotTimeout is longer than the default: building a package walks the
// whole register, and the default ten seconds would fail a healthy export on a
// site with any history.
const snapshotTimeout = 5 * time.Minute

// Snapshot is one catalogue entry.
type Snapshot struct {
	Tag         string         `json:"tag"`
	Kind        string         `json:"kind"`
	SizeBytes   int64          `json:"size_bytes"`
	Counts      map[string]int `json:"counts"`
	Notes       string         `json:"notes,omitempty"`
	GeneratedAt time.Time      `json:"generated_at"`
	Public      bool           `json:"public"`
}

// SnapshotCatalogue is the listing plus what the operator has enabled.
type SnapshotCatalogue struct {
	Snapshots   []Snapshot `json:"snapshots"`
	FullEnabled bool       `json:"full_enabled"`
}

// ListSnapshots returns the catalogue. An empty kind lists every kind.
func (c *Client) ListSnapshots(ctx context.Context, kind string) (SnapshotCatalogue, error) {
	path := "/v1/snapshots"
	if kind != "" {
		path += "?kind=" + url.QueryEscape(kind)
	}

	var catalogue SnapshotCatalogue
	if err := c.get(ctx, path, &catalogue); err != nil {
		return SnapshotCatalogue{}, err
	}
	return catalogue, nil
}

// CreateSnapshot generates and stores a package.
func (c *Client) CreateSnapshot(ctx context.Context, kind, tag, notes, actor string) (Snapshot, error) {
	body, err := json.Marshal(map[string]string{
		"kind": kind, "tag": tag, "notes": notes, "actor": actor,
	})
	if err != nil {
		return Snapshot{}, fmt.Errorf("encode snapshot request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, snapshotTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/v1/snapshots", bytes.NewReader(body))
	if err != nil {
		return Snapshot{}, fmt.Errorf("build snapshot request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return Snapshot{}, fmt.Errorf("call backend: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode >= http.StatusBadRequest {
		return Snapshot{}, fmt.Errorf("%s", backendError(resp))
	}

	var created Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return Snapshot{}, fmt.Errorf("decode snapshot response: %w", err)
	}
	return created, nil
}

// DownloadSnapshot fetches a package by tag.
func (c *Client) DownloadSnapshot(ctx context.Context, tag, actor string) ([]byte, error) {
	path := "/v1/snapshots/" + url.PathEscape(tag)
	if actor != "" {
		path += "?actor=" + url.QueryEscape(actor)
	}

	ctx, cancel := context.WithTimeout(ctx, snapshotTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build download request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call backend: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode >= http.StatusBadRequest {
		return nil, fmt.Errorf("%s", backendError(resp))
	}
	return io.ReadAll(resp.Body)
}

// DeleteSnapshot removes a package and its catalogue row.
func (c *Client) DeleteSnapshot(ctx context.Context, tag, actor string) error {
	path := "/v1/snapshots/" + url.PathEscape(tag)
	if actor != "" {
		path += "?actor=" + url.QueryEscape(actor)
	}
	return c.send(ctx, http.MethodDelete, path, "", nil)
}

// SnapshotRestoreResult reports what a restore covered.
type SnapshotRestoreResult struct {
	Kind     string         `json:"kind"`
	Counts   map[string]int `json:"counts"`
	Restored map[string]int `json:"restored"`
}

// RestoreSnapshot uploads a package and replaces what it covers.
func (c *Client) RestoreSnapshot(ctx context.Context, packageData []byte, actor string) (SnapshotRestoreResult, error) {
	path := "/v1/snapshots/restore"
	if actor != "" {
		path += "?actor=" + url.QueryEscape(actor)
	}

	ctx, cancel := context.WithTimeout(ctx, snapshotTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+path, bytes.NewReader(packageData))
	if err != nil {
		return SnapshotRestoreResult{}, fmt.Errorf("build restore request: %w", err)
	}
	req.Header.Set("Content-Type", "application/zip")

	resp, err := c.http.Do(req)
	if err != nil {
		return SnapshotRestoreResult{}, fmt.Errorf("call backend: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode >= http.StatusBadRequest {
		return SnapshotRestoreResult{}, fmt.Errorf("%s", backendError(resp))
	}

	var result SnapshotRestoreResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return SnapshotRestoreResult{}, fmt.Errorf("decode restore response: %w", err)
	}
	return result, nil
}
