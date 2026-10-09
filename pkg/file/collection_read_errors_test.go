// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fail once, then allow further reads. The collection reader must not try to
// recover, return partial chunk data, or restart the archive after this error.
type collectionReadFailure struct {
	reader     io.Reader
	remaining  int
	failure    error
	withBytes  bool
	failed     bool
	afterError int
}

func (r *collectionReadFailure) Read(p []byte) (int, error) {
	if r.failed {
		r.afterError++
		return r.reader.Read(p)
	}
	if r.remaining == 0 {
		r.failed = true
		return 0, r.failure
	}
	if len(p) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= n
	if err == nil && r.remaining == 0 && r.withBytes {
		r.failed = true
		return n, r.failure
	}
	return n, err
}

func TestTARCollectionReadFailure(t *testing.T) {
	ctx := context.Background()
	for _, format := range []Format{FormatBin, FormatPNG} {
		payload := []byte("valid first chunk")
		body, firstName, failedName := payload, "2A2_0001.bin", "2A2_0002.bin"
		if format == FormatPNG {
			var png bytes.Buffer
			if err := encodePNGWithData(&png, createSmallPNG(), payload); err != nil {
				t.Fatal(err)
			}
			body, firstName, failedName = png.Bytes(), "IMG2A2_0001.PNG", "IMG2A2_0002.PNG"
		}
		for _, skipped := range []bool{false, true} {
			name, operation := failedName, "read TAR entry"
			if skipped {
				name, operation = "ignored.txt", "skip TAR entry"
			}
			for _, failurePoint := range []struct {
				name      string
				progress  int
				withBytes bool
			}{
				{"before_body", 0, false},
				{"after_partial_body", 3, false},
				{"partial_body_with_error", 3, true},
				{"complete_body_with_error", len(body), true},
			} {
				t.Run(fmt.Sprintf("%s/skipped=%t/%s", format, skipped, failurePoint.name), func(t *testing.T) {
					var archive bytes.Buffer
					w := tar.NewWriter(&archive)
					bodyStart := 0
					for _, entry := range []string{firstName, name, "later.txt"} {
						if err := w.WriteHeader(&tar.Header{Name: entry, Mode: 0600, Size: int64(len(body))}); err != nil {
							t.Fatal(err)
						}
						if entry == name {
							bodyStart = archive.Len()
						}
						if _, err := w.Write(body); err != nil {
							t.Fatal(err)
						}
					}
					if err := w.Close(); err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(t.TempDir(), "renamed.tar")
					if err := os.WriteFile(path, archive.Bytes(), 0600); err != nil {
						t.Fatal(err)
					}
					f, err := os.Open(path)
					if err != nil {
						t.Fatal(err)
					}
					failure := errors.New("injected storage read failure")
					pathError := &os.PathError{Op: "read", Path: path, Err: failure}
					source := &collectionReadFailure{reader: f, remaining: bodyStart + failurePoint.progress,
						failure: pathError, withBytes: failurePoint.withBytes}
					reader := NewCollectionReader(Collection{Name: "2A2", Path: path, Format: format})
					reader.tarFile, reader.tarReader = f, tar.NewReader(source)
					defer reader.Close()
					if got, err := reader.ReadNextChunk(ctx); err != nil || !bytes.Equal(got, payload) {
						t.Fatalf("first chunk changed: %q, err=%v", got, err)
					}
					data, err := reader.ReadNextChunk(ctx)
					if !errors.Is(err, failure) || len(data) != 0 {
						t.Fatalf("read returned partial data or lost original error: %q, err=%v", data, err)
					}
					var gotPathError *os.PathError
					if !errors.As(err, &gotPathError) || gotPathError != pathError {
						t.Errorf("read lost filesystem error details: %v", err)
					}
					for _, want := range []string{operation, name, path} {
						if !strings.Contains(err.Error(), want) {
							t.Errorf("failure does not identify %q: %v", want, err)
						}
					}
					if reader.ChunkIndex != 2 || reader.tarFile != nil || reader.tarReader != nil {
						t.Errorf("failed reader advanced or retained archive state: chunk=%d, hasFile=%t, hasReader=%t", reader.ChunkIndex, reader.tarFile != nil, reader.tarReader != nil)
					}
					if _, err := f.Stat(); !errors.Is(err, os.ErrClosed) {
						t.Errorf("failed read left archive open: %v", err)
					}
					for i := 0; i < 2; i++ {
						if data, nextErr := reader.ReadNextChunk(ctx); nextErr != err || len(data) != 0 {
							t.Errorf("read after failure restarted archive: %q, err=%v; want %v", data, nextErr, err)
						}
					}
					if source.afterError != 0 {
						t.Errorf("continued reading source %d times after error", source.afterError)
					}
				})
			}
		}
	}
}

func TestTARCollectionEOFRemainsTerminal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "2A2.tar")
	if err := os.WriteFile(path, discoveryTestTAR(t, "2A2_0001.bin", []byte("chunk")), 0600); err != nil {
		t.Fatal(err)
	}
	reader := NewCollectionReader(Collection{Name: "2A2", Path: path, Format: FormatBin})
	defer reader.Close()
	if _, err := reader.ReadNextChunk(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if data, err := reader.ReadNextChunk(context.Background()); err != io.EOF || len(data) != 0 {
			t.Fatalf("EOF restarted archive: data=%q, err=%v", data, err)
		}
		if reader.tarFile != nil || reader.tarReader != nil {
			t.Fatal("EOF retained an archive handle/reader")
		}
	}
}

func TestTARCollectionInvalidEntryClosesReader(t *testing.T) {
	first := discoveryTestTAR(t, "2A2_0001.bin", []byte("first chunk"))
	first = first[:len(first)-1024] // Remove the end marker before appending damage.
	for _, tc := range []struct {
		name, message string
		tail          []byte
		cause         error
	}{
		{"header", "read TAR header", bytes.Repeat([]byte{0xff}, 512), tar.ErrHeader},
		{"truncated_header", "read TAR header", []byte("partial header"), io.ErrUnexpectedEOF},
		{"invalid_png", "decode PNG entry", discoveryTestTAR(t, "IMG2A2_0002.PNG", []byte("not a PNG")), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "2A2.tar")
			if err := os.WriteFile(path, append(bytes.Clone(first), tc.tail...), 0600); err != nil {
				t.Fatal(err)
			}
			reader := NewCollectionReader(Collection{Name: "2A2", Path: path})
			defer reader.Close()
			var opened *os.File
			if tc.name == "invalid_png" {
				// Indexing validates all headers first. PNG body validation still
				// happens when that chunk is consumed.
				if _, err := reader.ReadNextChunk(context.Background()); err != nil {
					t.Fatal(err)
				}
				opened = reader.tarFile
			}
			data, err := reader.ReadNextChunk(context.Background())
			if err == nil || len(data) != 0 || !strings.Contains(err.Error(), tc.message) || !strings.Contains(err.Error(), path) {
				t.Fatalf("entry failure missing or mislabeled: data=%q, err=%v", data, err)
			}
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Errorf("lost original cause %v: %v", tc.cause, err)
			}
			if opened != nil {
				if _, err := opened.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Errorf("invalid entry left archive open: %v", err)
				}
			}
			if reader.tarFile != nil || reader.tarReader != nil || reader.tarChunks != nil {
				t.Error("invalid entry retained archive state")
			}
			if data, nextErr := reader.ReadNextChunk(context.Background()); nextErr != err || len(data) != 0 {
				t.Fatalf("invalid entry was skipped or archive restarted: %q, %v", data, nextErr)
			}
		})
	}
}

func TestTARCollectionCloseFailureIsNotEOF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "2A2.tar")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	// Use a closed handle to make cleanup fail after an otherwise normal EOF.
	reader := NewCollectionReader(Collection{Name: "2A2", Path: path, Format: FormatBin})
	reader.tarFile = f
	reader.tarReader = tar.NewReader(bytes.NewReader(discoveryTestTAR(t, "2A2_0001.bin", []byte("chunk"))))
	if _, err := reader.ReadNextChunk(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err = reader.ReadNextChunk(context.Background())
	if err == io.EOF || !errors.Is(err, io.EOF) || !errors.Is(err, os.ErrClosed) || !strings.Contains(err.Error(), "close TAR archive") {
		t.Fatalf("close failure was lost or became normal EOF: %v", err)
	}
}
