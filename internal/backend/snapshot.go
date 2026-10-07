package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/rs/zerolog/log"

	"github.com/CoderSyndicate/doleances/internal/cache"
	"github.com/CoderSyndicate/doleances/internal/models"
	"github.com/CoderSyndicate/doleances/internal/snapshot"
	"github.com/CoderSyndicate/doleances/internal/store"
)

func (a *API) registerSnapshotRoutes(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-snapshots",
		Method:      http.MethodGet,
		Path:        "/v1/snapshots",
		Summary:     "List stored snapshots",
		Description: "The catalogue of generated exports. Filter by kind with ?kind=contributions.",
		Tags:        []string{"Snapshots"},
	}, a.listSnapshots)

	huma.Register(api, huma.Operation{
		OperationID: "create-snapshot",
		Method:      http.MethodPost,
		Path:        "/v1/snapshots",
		Summary:     "Generate a snapshot",
		Description: "Builds a package from live data and stores it. " +
			"The full kind contains personal data and is refused unless the operator enabled it.",
		Tags: []string{"Snapshots"},
	}, a.createSnapshot)

	huma.Register(api, huma.Operation{
		OperationID: "download-snapshot",
		Method:      http.MethodGet,
		Path:        "/v1/snapshots/{tag}",
		Summary:     "Download a snapshot package",
		Tags:        []string{"Snapshots"},
	}, a.downloadSnapshot)

	huma.Register(api, huma.Operation{
		OperationID: "delete-snapshot",
		Method:      http.MethodDelete,
		Path:        "/v1/snapshots/{tag}",
		Summary:     "Delete a snapshot",
		Tags:        []string{"Snapshots"},
	}, a.deleteSnapshot)

	huma.Register(api, invalidates(cache.Messages, cache.Groups, cache.Actions, cache.Historical, cache.Subjects)(huma.Operation{
		OperationID: "restore-snapshot",
		Method:      http.MethodPost,
		Path:        "/v1/snapshots/restore",
		Summary:     "Restore a snapshot package",
		Description: "Replaces what the package covers. A contributions package replaces the " +
			"register only; groups, actions and participants are untouched unless the package is full.",
		Tags: []string{"Snapshots"},
	}), a.restoreSnapshot)
}

// SnapshotItem is one catalogue row as the API reports it.
type SnapshotItem struct {
	Tag         string         `json:"tag"`
	Kind        string         `json:"kind"`
	SizeBytes   int64          `json:"size_bytes"`
	Counts      map[string]int `json:"counts"`
	Notes       string         `json:"notes,omitempty"`
	GeneratedAt time.Time      `json:"generated_at"`
	Public      bool           `json:"public"`
}

// SnapshotListOutput is the catalogue.
type SnapshotListOutput struct {
	Body struct {
		Snapshots []SnapshotItem `json:"snapshots"`

		// FullEnabled tells the console whether to offer the full export at
		// all, rather than letting somebody press a button that always fails.
		FullEnabled bool `json:"full_enabled"`
	}
}

// SnapshotListInput narrows the catalogue to one kind.
type SnapshotListInput struct {
	Kind string `query:"kind" doc:"contributions or full; omit for both"`
}

func (a *API) listSnapshots(ctx context.Context, in *SnapshotListInput) (*SnapshotListOutput, error) {
	var kind models.SnapshotKind
	if in.Kind != "" {
		parsed, err := snapshot.ParseKind(in.Kind)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity(err.Error())
		}
		kind = models.SnapshotKind(parsed)
	}

	rows, err := a.store.ListSnapshots(ctx, kind)
	if err != nil {
		log.Error().Err(err).Msg("cannot list snapshots")
		return nil, huma.Error500InternalServerError("cannot list snapshots")
	}

	out := &SnapshotListOutput{}
	out.Body.FullEnabled = a.storageConfig.SnapshotFullEnabled
	out.Body.Snapshots = make([]SnapshotItem, 0, len(rows))
	for _, row := range rows {
		out.Body.Snapshots = append(out.Body.Snapshots, SnapshotItem{
			Tag:         row.Tag,
			Kind:        string(row.Kind),
			SizeBytes:   row.SizeBytes,
			Counts:      store.DecodeCounts(row.Counts),
			Notes:       row.Notes,
			GeneratedAt: row.GeneratedAt,
			Public:      row.Public(),
		})
	}
	return out, nil
}

// SnapshotCreateInput asks for a new package.
type SnapshotCreateInput struct {
	Body struct {
		Kind  string `json:"kind" doc:"contributions or full"`
		Tag   string `json:"tag,omitempty" doc:"a name; generated from the time when empty"`
		Notes string `json:"notes,omitempty" doc:"why this snapshot was taken"`
		Actor string `json:"actor,omitempty" doc:"the console identity requesting it"`
	}
}

// SnapshotCreateOutput reports what was stored.
type SnapshotCreateOutput struct {
	Body SnapshotItem
}

// tagPattern keeps a tag usable as both a URL segment and a storage key.
var tagPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,128}$`)

func (a *API) createSnapshot(ctx context.Context, in *SnapshotCreateInput) (*SnapshotCreateOutput, error) {
	kind, err := snapshot.ParseKind(in.Body.Kind)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error())
	}

	// The export that carries personal data stays off unless an operator
	// turned it on: reaching the console must not by itself be enough to dump
	// the participant database.
	if kind == snapshot.KindFull && !a.storageConfig.SnapshotFullEnabled {
		return nil, huma.Error403Forbidden(
			"the full snapshot contains personal data and is disabled; " +
				"enable it with --snapshot-full-enabled")
	}

	now := time.Now().UTC()
	tag := in.Body.Tag
	if tag == "" {
		tag = string(kind) + "-" + now.Format("20060102T150405Z")
	}
	if !tagPattern.MatchString(tag) {
		return nil, huma.Error422UnprocessableEntity(
			"a tag may hold letters, digits, dot, dash and underscore only")
	}
	if _, err := a.store.GetSnapshot(ctx, tag); err == nil {
		return nil, huma.Error409Conflict("a snapshot already carries that tag")
	} else if !errors.Is(err, store.ErrSnapshotNotFound) {
		return nil, huma.Error500InternalServerError("cannot check the tag")
	}

	// Built in memory because the bytes have to be written to storage and
	// measured before the catalogue row can exist. The register is text, and
	// text compresses; when that stops being true this becomes a temp file.
	var buf bytes.Buffer
	manifest, err := snapshot.Build(ctx, &buf, a.store.SnapshotSource(), snapshot.Options{
		Kind:      kind,
		Now:       now,
		Generator: a.generator,
	})
	if err != nil {
		log.Error().Err(err).Str("kind", string(kind)).Msg("cannot build snapshot")
		return nil, huma.Error500InternalServerError("cannot build the snapshot")
	}

	key := snapshotKey(kind, tag)
	if err := a.storage.Write(ctx, key, buf.Bytes()); err != nil {
		log.Error().Err(err).Str("key", key).Msg("cannot store snapshot")
		return nil, huma.Error500InternalServerError("cannot store the snapshot")
	}

	row := models.Snapshot{
		Tag:         tag,
		Kind:        models.SnapshotKind(kind),
		StorageKey:  key,
		SizeBytes:   int64(buf.Len()),
		Counts:      store.EncodeCounts(manifest.Counts),
		Notes:       in.Body.Notes,
		GeneratedAt: manifest.GeneratedAt,
	}
	if err := a.store.SaveSnapshot(ctx, row); err != nil {
		// The object is already written; without a row nothing can find it,
		// so remove it rather than leave an orphan nobody will ever reclaim.
		if cleanup := a.storage.Delete(ctx, key); cleanup != nil {
			log.Warn().Err(cleanup).Str("key", key).Msg("orphaned snapshot object")
		}
		log.Error().Err(err).Msg("cannot record snapshot")
		return nil, huma.Error500InternalServerError("cannot record the snapshot")
	}

	a.recordDecision(ctx, in.Body.Actor, models.AuditConfigChange, "snapshot", tag,
		fmt.Sprintf("generated %s snapshot", kind))
	a.pruneSnapshots(ctx, models.SnapshotKind(kind))

	log.Info().Str("tag", tag).Str("kind", string(kind)).
		Int("bytes", buf.Len()).Msg("snapshot generated")

	return &SnapshotCreateOutput{Body: SnapshotItem{
		Tag:         row.Tag,
		Kind:        string(row.Kind),
		SizeBytes:   row.SizeBytes,
		Counts:      manifest.Counts,
		Notes:       row.Notes,
		GeneratedAt: row.GeneratedAt,
		Public:      row.Public(),
	}}, nil
}

// SnapshotTagInput addresses one snapshot.
type SnapshotTagInput struct {
	Tag   string `path:"tag"`
	Actor string `query:"actor" doc:"the console identity performing the action"`
}

// SnapshotDownloadOutput carries the package bytes.
type SnapshotDownloadOutput struct {
	ContentType        string `header:"Content-Type"`
	ContentDisposition string `header:"Content-Disposition"`
	Body               []byte
}

func (a *API) downloadSnapshot(ctx context.Context, in *SnapshotTagInput) (*SnapshotDownloadOutput, error) {
	row, err := a.store.GetSnapshot(ctx, in.Tag)
	if errors.Is(err, store.ErrSnapshotNotFound) {
		return nil, huma.Error404NotFound("no such snapshot")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("cannot read the snapshot")
	}

	data, err := a.storage.Read(ctx, row.StorageKey)
	if err != nil {
		log.Error().Err(err).Str("tag", in.Tag).Msg("cannot read snapshot object")
		return nil, huma.Error500InternalServerError("cannot read the snapshot")
	}

	// A download of personal data is an event worth recording; a download of
	// the public register is not, and logging it would be surveillance of the
	// very people the export exists to serve.
	if !row.Public() {
		a.recordDecision(ctx, in.Actor, models.AuditConfigChange, "snapshot", row.Tag,
			"downloaded the full snapshot")
	}

	return &SnapshotDownloadOutput{
		ContentType:        "application/zip",
		ContentDisposition: `attachment; filename="` + row.Tag + `.zip"`,
		Body:               data,
	}, nil
}

// SnapshotDeleteOutput is empty; the status is the answer.
type SnapshotDeleteOutput struct {
	Status int
}

func (a *API) deleteSnapshot(ctx context.Context, in *SnapshotTagInput) (*SnapshotDeleteOutput, error) {
	key, err := a.store.DeleteSnapshot(ctx, in.Tag)
	if errors.Is(err, store.ErrSnapshotNotFound) {
		return nil, huma.Error404NotFound("no such snapshot")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("cannot delete the snapshot")
	}
	if err := a.storage.Delete(ctx, key); err != nil {
		log.Warn().Err(err).Str("key", key).Msg("snapshot object not removed")
	}

	a.recordDecision(ctx, in.Actor, models.AuditDelete, "snapshot", in.Tag, "deleted a snapshot")
	return &SnapshotDeleteOutput{Status: http.StatusNoContent}, nil
}

// SnapshotRestoreInput carries an uploaded package.
type SnapshotRestoreInput struct {
	Actor   string `query:"actor"`
	RawBody []byte
}

// SnapshotRestoreOutput reports what the restore covered.
type SnapshotRestoreOutput struct {
	Body struct {
		Kind     string         `json:"kind"`
		Counts   map[string]int `json:"counts"`
		Restored map[string]int `json:"restored"`
	}
}

func (a *API) restoreSnapshot(ctx context.Context, in *SnapshotRestoreInput) (*SnapshotRestoreOutput, error) {
	pkg, err := snapshot.Read(in.RawBody)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error())
	}

	// A full package rewrites participants and the audit log, so it is gated
	// by the same switch that gates producing one.
	if pkg.Manifest.Kind == snapshot.KindFull && !a.storageConfig.SnapshotFullEnabled {
		return nil, huma.Error403Forbidden(
			"restoring a full snapshot is disabled; enable it with --snapshot-full-enabled")
	}

	if err := a.store.RestoreSnapshot(ctx, pkg); err != nil {
		log.Error().Err(err).Msg("restore failed")
		return nil, huma.Error500InternalServerError("the restore failed and nothing was changed")
	}

	a.recordDecision(ctx, in.Actor, models.AuditConfigChange, "snapshot", string(pkg.Manifest.Kind),
		"restored a "+string(pkg.Manifest.Kind)+" snapshot")

	out := &SnapshotRestoreOutput{}
	out.Body.Kind = string(pkg.Manifest.Kind)
	out.Body.Counts = pkg.Manifest.Counts
	out.Body.Restored = map[string]int{
		"messages":   len(pkg.Messages),
		"subjects":   len(pkg.Subjects),
		"historical": len(pkg.Historical),
	}
	if pkg.Manifest.Kind == snapshot.KindFull {
		out.Body.Restored["groups"] = len(pkg.Groups)
		out.Body.Restored["actions"] = len(pkg.Actions)
		out.Body.Restored["audit_entries"] = len(pkg.AuditEntries)
	}

	log.Warn().Str("kind", string(pkg.Manifest.Kind)).
		Interface("restored", out.Body.Restored).Msg("snapshot restored — data replaced")
	return out, nil
}

// pruneSnapshots enforces retention after a new snapshot is stored. A failure
// here is logged and not returned: the snapshot the operator asked for exists,
// and refusing it because an old one could not be removed helps nobody.
func (a *API) pruneSnapshots(ctx context.Context, kind models.SnapshotKind) {
	keys, err := a.store.PruneSnapshots(ctx, kind, a.storageConfig.SnapshotRetention)
	if err != nil {
		log.Warn().Err(err).Msg("snapshot retention sweep failed")
		return
	}
	for _, key := range keys {
		if err := a.storage.Delete(ctx, key); err != nil {
			log.Warn().Err(err).Str("key", key).Msg("pruned snapshot object not removed")
		}
	}
	if len(keys) > 0 {
		log.Info().Int("removed", len(keys)).Str("kind", string(kind)).Msg("snapshots pruned")
	}
}

// snapshotKey is where a package lives in storage. The kind is part of the
// path so that a bucket listing shows at a glance what is personal data.
func snapshotKey(kind snapshot.Kind, tag string) string {
	return "snapshots/" + string(kind) + "/" + tag + ".zip"
}
