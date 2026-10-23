package audit

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Destination is the panel-locked off-host export boundary
// (FEATURE_OFFHOST_BACKUP.md). Lane 1 may eventually consolidate this
// with internal/backup; Lane 4 ships a pluggable boundary with two
// implementations: file:// and a memory destination used by tests.
//
// Implementations MUST be safe to call once per destination URL.
type Destination interface {
	// URL returns the canonical destination URL (e.g. file:///path,
	// s3://bucket/key, sftp://host/path). Used by audit emit metadata.
	URL() string
	// Open returns an io.WriteCloser that the caller writes JSONL bytes
	// to. The caller is responsible for invoking Close when done; an
	// error from Close MUST be propagated.
	Open(ctx context.Context) (io.WriteCloser, error)
}

// FileDestination writes the export to a local file. Url is
// "file:///<absolute path>".
type FileDestination struct {
	Path string
}

// URL implements Destination.
func (d FileDestination) URL() string {
	abs, _ := filepath.Abs(d.Path)
	return "file://" + filepath.ToSlash(abs)
}

// Open implements Destination.
func (d FileDestination) Open(_ context.Context) (io.WriteCloser, error) {
	if err := os.MkdirAll(filepath.Dir(d.Path), 0o750); err != nil {
		return nil, fmt.Errorf("audit: mkdir %s: %w", filepath.Dir(d.Path), err)
	}
	f, err := os.OpenFile(d.Path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("audit: open %s: %w", d.Path, err)
	}
	return f, nil
}

// MemoryDestination is a Destination backed by an in-memory buffer.
// Used by tests; the URL is "memory://<label>".
type MemoryDestination struct {
	Label string
	buf   *closableBuffer
}

// NewMemoryDestination constructs a fresh MemoryDestination.
func NewMemoryDestination(label string) *MemoryDestination {
	return &MemoryDestination{Label: label, buf: &closableBuffer{}}
}

// URL implements Destination.
func (d *MemoryDestination) URL() string { return "memory://" + d.Label }

// Open implements Destination.
func (d *MemoryDestination) Open(_ context.Context) (io.WriteCloser, error) {
	return d.buf, nil
}

// Bytes returns the captured bytes. Safe to call after Open's writer
// has been closed.
func (d *MemoryDestination) Bytes() []byte {
	return d.buf.Bytes()
}

type closableBuffer struct {
	data []byte
}

func (b *closableBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	return len(p), nil
}
func (b *closableBuffer) Close() error  { return nil }
func (b *closableBuffer) Bytes() []byte { return b.data }

// ParseDestination returns a Destination for the supplied URL. Lane 4
// supports file:// only; s3://, gcs://, sftp:// return an explicit
// "not yet implemented" sentinel so the CLI can surface the gap.
func ParseDestination(u string) (Destination, error) {
	switch {
	case strings.HasPrefix(u, "file://"):
		return FileDestination{Path: filepath.FromSlash(strings.TrimPrefix(u, "file://"))}, nil
	case strings.HasPrefix(u, "memory://"):
		return NewMemoryDestination(strings.TrimPrefix(u, "memory://")), nil
	case !strings.Contains(u, "://"):
		// Bare path — treat as file://.
		return FileDestination{Path: u}, nil
	case strings.HasPrefix(u, "s3://"), strings.HasPrefix(u, "gcs://"), strings.HasPrefix(u, "sftp://"), strings.HasPrefix(u, "azure://"):
		return nil, ErrDestinationNotSupported
	default:
		return nil, fmt.Errorf("audit: unknown destination scheme: %s", u)
	}
}

// ErrDestinationNotSupported is returned for off-host destinations that
// Lane 4 has stubbed but not yet implemented.
var ErrDestinationNotSupported = errors.New("audit: off-host destination not implemented in Lane 4 (TODO: consolidate with internal/backup Destination)")

// Exporter streams JSONL audit records to a Destination.
type Exporter struct {
	chain AuditChain
}

// NewExporter constructs an Exporter against the supplied chain.
func NewExporter(chain AuditChain) *Exporter { return &Exporter{chain: chain} }

// Export streams every row + every checkpoint as JSONL to dst, then
// writes a trailing manifest line. Returns the manifest summarizing the
// run.
func (e *Exporter) Export(ctx context.Context, dst Destination) (Manifest, error) {
	rows, err := e.chain.Read(ctx, Query{})
	if err != nil {
		return Manifest{}, err
	}
	checkpoints, err := e.chain.ListCheckpoints(ctx)
	if err != nil {
		return Manifest{}, err
	}
	wc, err := dst.Open(ctx)
	if err != nil {
		return Manifest{}, err
	}
	bw := bufio.NewWriter(wc)
	streamHash := sha256.New()
	mw := io.MultiWriter(bw, streamHash)

	manifest := Manifest{
		GenesisHashHex:       hex.EncodeToString(GenesisHash()),
		CanonicalFormVersion: CanonicalFormVersion,
	}

	for i := range rows {
		row := rows[i]
		rec := JSONLRecord{Kind: RecordKindRow, Row: &row}
		buf, err := json.Marshal(rec)
		if err != nil {
			_ = wc.Close()
			return Manifest{}, fmt.Errorf("audit: encode row: %w", err)
		}
		if _, err := mw.Write(buf); err != nil {
			_ = wc.Close()
			return Manifest{}, err
		}
		if _, err := mw.Write([]byte("\n")); err != nil {
			_ = wc.Close()
			return Manifest{}, err
		}
		manifest.RowCount++
	}
	for i := range checkpoints {
		cp := checkpoints[i]
		rec := JSONLRecord{Kind: RecordKindCheckpoint, Checkpoint: &cp}
		buf, err := json.Marshal(rec)
		if err != nil {
			_ = wc.Close()
			return Manifest{}, fmt.Errorf("audit: encode checkpoint: %w", err)
		}
		if _, err := mw.Write(buf); err != nil {
			_ = wc.Close()
			return Manifest{}, err
		}
		if _, err := mw.Write([]byte("\n")); err != nil {
			_ = wc.Close()
			return Manifest{}, err
		}
		manifest.CheckpointCount++
		if manifest.FromCheckpointID == 0 {
			manifest.FromCheckpointID = cp.ID
		}
		manifest.ToCheckpointID = cp.ID
		if cp.SigningKeyFingerprint != "" {
			manifest.SigningKeyFingerprint = cp.SigningKeyFingerprint
		}
	}
	manifest.StreamSHA256 = hex.EncodeToString(streamHash.Sum(nil))
	rec := JSONLRecord{Kind: RecordKindManifest, Manifest: &manifest}
	buf, err := json.Marshal(rec)
	if err != nil {
		_ = wc.Close()
		return Manifest{}, fmt.Errorf("audit: encode manifest: %w", err)
	}
	if _, err := bw.Write(buf); err != nil {
		_ = wc.Close()
		return Manifest{}, err
	}
	if _, err := bw.Write([]byte("\n")); err != nil {
		_ = wc.Close()
		return Manifest{}, err
	}
	if err := bw.Flush(); err != nil {
		_ = wc.Close()
		return Manifest{}, err
	}
	if err := wc.Close(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// JSONLSource reads a JSONL export back into Source-shaped slices.
type JSONLSource struct {
	rows        []Row
	checkpoints []Checkpoint
	manifest    Manifest
}

// NewJSONLSourceFromReader parses every JSONL line from r.
func NewJSONLSourceFromReader(r io.Reader) (*JSONLSource, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	src := &JSONLSource{}
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec JSONLRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, fmt.Errorf("audit: parse jsonl line: %w", err)
		}
		switch rec.Kind {
		case RecordKindRow:
			if rec.Row == nil {
				return nil, fmt.Errorf("audit: row record has no row payload")
			}
			src.rows = append(src.rows, *rec.Row)
		case RecordKindCheckpoint:
			if rec.Checkpoint == nil {
				return nil, fmt.Errorf("audit: checkpoint record has no checkpoint payload")
			}
			src.checkpoints = append(src.checkpoints, *rec.Checkpoint)
		case RecordKindManifest:
			if rec.Manifest != nil {
				src.manifest = *rec.Manifest
			}
		default:
			// Ignore unknown record kinds for forward compatibility.
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return src, nil
}

// NewJSONLSourceFromFile is a convenience wrapper for reading from disk.
func NewJSONLSourceFromFile(path string) (*JSONLSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return NewJSONLSourceFromReader(f)
}

// Rows implements Source.
func (s *JSONLSource) Rows(_ context.Context) ([]Row, error) { return cloneRows(s.rows), nil }

// Checkpoints implements Source.
func (s *JSONLSource) Checkpoints(_ context.Context) ([]Checkpoint, error) {
	return cloneCheckpoints(s.checkpoints), nil
}

// Manifest returns the parsed manifest, or the zero value if the JSONL
// did not include one.
func (s *JSONLSource) Manifest() Manifest { return s.manifest }

func cloneRows(in []Row) []Row {
	out := make([]Row, len(in))
	for i, r := range in {
		out[i] = copyRow(r)
	}
	return out
}

func cloneCheckpoints(in []Checkpoint) []Checkpoint {
	out := make([]Checkpoint, len(in))
	for i, cp := range in {
		out[i] = copyCheckpoint(cp)
	}
	return out
}
