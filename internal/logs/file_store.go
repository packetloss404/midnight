package logs

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/redaction"
)

// FileStore is a paginated, structured-filter NDJSON log store backed by one
// or more append-only files. Files are read in lexical order so callers that
// rotate logs by name (e.g., `app.log`, `app.log.1`) get deterministic
// ordering. The store performs no writes; it is read-only over fixtures.
//
// When a *redaction.Redactor is wired in via WithRedactor, every entry's
// Message and string-valued Fields are redacted before filtering or returning
// it. Queries must never become an oracle for the presence of raw secrets.
type FileStore struct {
	dir         string
	patternGlob string
	redactor    *redaction.Redactor
}

// FileStoreOption configures a FileStore at construction time.
type FileStoreOption func(*FileStore)

// WithRedactor installs r as the read-path redactor. When non-nil, every
// entry considered by Query has its Message and string-valued Fields run
// through the redactor's rule set before any free-text filtering.
func WithRedactor(r *redaction.Redactor) FileStoreOption {
	return func(store *FileStore) {
		store.redactor = r
	}
}

// NewFileStore constructs a FileStore that searches dir for files matching
// pattern. Pattern accepts standard `path/filepath.Match` globs; "*" matches
// every file. The directory is not opened until Query is called.
func NewFileStore(dir, pattern string, opts ...FileStoreOption) (*FileStore, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("log file store directory is required")
	}
	if pattern == "" {
		pattern = "*"
	}
	store := &FileStore{dir: dir, patternGlob: pattern}
	for _, opt := range opts {
		if opt != nil {
			opt(store)
		}
	}
	return store, nil
}

// Redactor returns the redactor configured via WithRedactor, or nil when
// none is installed. The returned redactor is shared with the store; do not
// mutate.
func (store *FileStore) Redactor() *redaction.Redactor {
	if store == nil {
		return nil
	}
	return store.redactor
}

// Query returns one page of log entries that satisfy filter. When cursor is
// non-zero, scanning resumes from cursor.File at cursor.Offset (zero-based
// line index). Pagination is line-based; pageSize sets the maximum entries to
// return. When pageSize <= 0 it defaults to 200.
//
// When a redactor was supplied via WithRedactor, every decoded entry is
// redacted before its filters run and before it is surfaced to the caller.
func (store *FileStore) Query(filter Filter, cursor Cursor, pageSize int) (QueryResult, error) {
	if store == nil {
		return QueryResult{}, errors.New("log file store is nil")
	}
	if err := filter.Validate(); err != nil {
		return QueryResult{}, failurecodes.Wrap(failurecodes.LogQueryInvalid, "filter validation failed", err)
	}
	if pageSize <= 0 {
		pageSize = 200
	}

	files, err := store.listFiles()
	if err != nil {
		return QueryResult{}, err
	}

	startFile := cursor.File
	startOffset := cursor.Offset
	if startOffset < 0 {
		startOffset = 0
	}
	startedScanning := startFile == ""

	result := QueryResult{Entries: make([]Entry, 0, pageSize)}
	for fileIdx, file := range files {
		if !startedScanning {
			if file != startFile {
				continue
			}
			startedScanning = true
		}

		offset := 0
		if file == startFile {
			offset = startOffset
		}
		entries, lastReadOffset, eof, err := readEntriesFrom(file, offset, filter, pageSize-len(result.Entries), store.redactor)
		if err != nil {
			return QueryResult{}, err
		}
		result.Entries = append(result.Entries, entries...)

		// If we filled the page, decide whether more entries remain.
		if len(result.Entries) >= pageSize {
			isLastFile := fileIdx == len(files)-1
			// If we hit EOF inside this file and there are no more files,
			// the page is the final one — not truncated.
			if eof && isLastFile {
				return result, nil
			}
			if eof {
				// More files remain; resume from the next file's offset 0.
				result.NextCursor = Cursor{File: files[fileIdx+1], Offset: 0}
			} else {
				result.NextCursor = Cursor{File: file, Offset: lastReadOffset + 1}
			}
			result.Truncated = true
			return result, nil
		}
		// Otherwise we exhausted the file. Move to the next one.
		_ = eof
	}

	return result, nil
}

func (store *FileStore) listFiles() ([]string, error) {
	pattern := filepath.Join(store.dir, store.patternGlob)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("log file glob %q: %w", pattern, err)
	}
	sort.Strings(matches)
	return matches, nil
}

func readEntriesFrom(path string, offset int, filter Filter, limit int, redactor *redaction.Redactor) ([]Entry, int, bool, error) {
	if limit <= 0 {
		return nil, offset - 1, false, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, false, fmt.Errorf("open log file %q: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	reader := bufio.NewReader(file)
	current := 0
	out := make([]Entry, 0, limit)
	lastIndex := offset - 1
	for {
		line, err := reader.ReadString('\n')
		if line == "" && err == io.EOF {
			return out, lastIndex, true, nil
		}
		if err != nil && err != io.EOF {
			return nil, lastIndex, false, fmt.Errorf("read log file %q: %w", path, err)
		}
		line = strings.TrimRight(line, "\r\n")
		if current < offset {
			current++
			if err == io.EOF {
				return out, lastIndex, true, nil
			}
			continue
		}
		if line != "" {
			var entry Entry
			if decodeErr := json.Unmarshal([]byte(line), &entry); decodeErr == nil {
				if redactor != nil {
					entry = RedactWith(redactor, entry)
				}
				if filter.Match(entry) {
					out = append(out, entry)
					lastIndex = current
					if len(out) >= limit {
						// Peek ahead to determine whether more entries remain
						// in the same file so callers can correctly set
						// Truncated for the cross-file pagination contract.
						peeked, peekErr := reader.ReadString('\n')
						hasMore := strings.TrimRight(peeked, "\r\n") != "" ||
							(peekErr != io.EOF && peekErr == nil)
						return out, lastIndex, !hasMore, nil
					}
				} else {
					lastIndex = current
				}
			} else {
				// Skip malformed lines but still advance the cursor.
				lastIndex = current
			}
		}
		current++
		if err == io.EOF {
			return out, lastIndex, true, nil
		}
	}
}

// EncodeFixtureNDJSON writes the entries to writer as NDJSON in input order
// without applying redaction. It is intended for synthetic fixture authoring.
func EncodeFixtureNDJSON(writer io.Writer, entries []Entry) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	for _, entry := range entries {
		if err := encoder.Encode(entry); err != nil {
			return err
		}
	}
	return nil
}
