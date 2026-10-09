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
	"testing"
)

func TestFindCollectionsDoesNotExtract(t *testing.T) {
	ctx := context.Background()
	for _, format := range []Format{FormatBin, FormatPNG} {
		for _, renamed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/renamed=%t", format, renamed), func(t *testing.T) {
				input, scratch := t.TempDir(), t.TempDir()
				for _, variable := range []string{"TMPDIR", "TMP", "TEMP"} {
					t.Setenv(variable, scratch)
				}
				dir := filepath.Join(input, "2A3")
				chunks := [][]byte{[]byte("first chunk"), []byte("second chunk")}
				for i, chunk := range chunks {
					if err := WriteNamedChunk(ctx, GetFormatter(format), dir, "2A3", i+1, chunk); err != nil {
						t.Fatal(err)
					}
				}
				archive, err := TarCollection(ctx, dir)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
				if renamed {
					newPath := filepath.Join(input, "renamed.tar")
					if err := os.Rename(archive, newPath); err != nil {
						t.Fatal(err)
					}
					archive = newPath
				}
				unrelated := discoveryTestTAR(t, "unrelated.bin", bytes.Repeat([]byte("x"), 32<<10))
				if err := os.WriteFile(filepath.Join(input, "other-backup.tar"), unrelated, 0600); err != nil {
					t.Fatal(err)
				}
				collections, tempDir, err := FindCollections(ctx, input)
				if tempDir != "" {
					t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
					t.Errorf("discovery allocated temporary extraction directory %q", tempDir)
				}
				if err != nil || len(collections) != 1 {
					t.Fatalf("discovered %v, err=%v; want one collection", collections, err)
				}
				want := Collection{Name: "2A3", Path: archive, Format: format}
				if collections[0] != want {
					t.Errorf("discovered %+v, want direct TAR access %+v", collections[0], want)
				}
				if entries, err := os.ReadDir(scratch); err != nil || len(entries) != 0 {
					t.Errorf("discovery wrote temporary files: entries=%v, err=%v", entries, err)
				}
				reader := NewCollectionReader(collections[0])
				defer reader.Close()
				for i, want := range chunks {
					got, err := reader.ReadNextChunk(ctx)
					if err != nil || !bytes.Equal(got, want) {
						t.Fatalf("chunk %d: got %q, err=%v; want %q", i+1, got, err, want)
					}
				}
				if _, err := reader.ReadNextChunk(ctx); err != io.EOF {
					t.Fatalf("expected end of collection, got %v", err)
				}
			})
		}
	}
}

func TestFindCollectionsUnrelatedArchiveLeavesNoFiles(t *testing.T) {
	input, scratch := t.TempDir(), t.TempDir()
	for _, variable := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(variable, scratch)
	}
	if err := os.WriteFile(filepath.Join(input, "unrelated.tar"), discoveryTestTAR(t, "photos/image.png", []byte("unrelated")), 0600); err != nil {
		t.Fatal(err)
	}
	collections, tempDir, err := FindCollections(context.Background(), input)
	if tempDir != "" {
		t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
	}
	if err == nil || len(collections) != 0 || tempDir != "" {
		t.Fatalf("unrelated archive discovered or extracted: collections=%v, temp=%q, err=%v", collections, tempDir, err)
	}
	if entries, err := os.ReadDir(scratch); err != nil || len(entries) != 0 {
		t.Fatalf("discovery left temporary files: entries=%v, err=%v", entries, err)
	}
}

func discoveryTestTAR(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var archive bytes.Buffer
	w := tar.NewWriter(&archive)
	if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

// A virtual ordinary TAR entry exercises offsets above 32 bits without storing
// or allocating the body. This is not a GNU/PAX sparse entry.
type discoveryLargeTAR struct {
	header, tail []byte
	tailOffset   int64
}

func (r discoveryLargeTAR) ReadAt(p []byte, offset int64) (int, error) {
	remaining := r.tailOffset + int64(len(r.tail)) - offset
	if remaining <= 0 {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > remaining {
		n = int(remaining)
	}
	clear(p[:n])
	if offset < int64(len(r.header)) {
		copy(p[:n], r.header[offset:])
	}
	if offset >= r.tailOffset {
		copy(p[:n], r.tail[offset-r.tailOffset:])
	} else if offset+int64(n) > r.tailOffset {
		copy(p[r.tailOffset-offset:n], r.tail)
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

type discoveryCountReader struct {
	io.ReadSeeker
	readBytes int64
	seeks     int
}

func (r *discoveryCountReader) Read(p []byte) (int, error) {
	n, err := r.ReadSeeker.Read(p)
	r.readBytes += int64(n)
	return n, err
}

func (r *discoveryCountReader) Seek(offset int64, whence int) (int64, error) {
	r.seeks++
	return r.ReadSeeker.Seek(offset, whence)
}

func TestProbeTarCollectionSkipsLargeUnrelatedBody(t *testing.T) {
	const bodySize int64 = 8 << 30
	var header bytes.Buffer
	w := tar.NewWriter(&header)
	if err := w.WriteHeader(&tar.Header{Name: "unrelated.dat", Size: bodySize, Mode: 0600, Format: tar.FormatGNU}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		member string
		format Format
	}{
		{"notes.txt", ""},
		{"2A3_0001.BiN", FormatBin},
		{"nested/IMG2A3_0001.pNg", FormatPNG},
	} {
		t.Run(tc.member, func(t *testing.T) {
			archive := discoveryLargeTAR{
				header: header.Bytes(), tailOffset: int64(header.Len()) + bodySize,
				tail: discoveryTestTAR(t, tc.member, []byte("chunk")),
			}
			r := &discoveryCountReader{ReadSeeker: io.NewSectionReader(archive, 0, archive.tailOffset+int64(len(archive.tail)))}
			collection, err := probeTarCollection(context.Background(), r, "renamed.tar")
			if err != nil {
				t.Fatal(err)
			}
			want := Collection{}
			if tc.format != "" {
				want = Collection{Name: "2A3", Path: "renamed.tar", Format: tc.format}
			}
			if collection != want {
				t.Fatalf("got %+v, want %+v", collection, want)
			}
			if r.seeks == 0 || r.readBytes > 4096 {
				t.Fatalf("probe consumed ordinary file bodies: read=%d, seeks=%d", r.readBytes, r.seeks)
			}
			t.Logf("8 GiB ordinary body skipped: %d bytes read, %d seeks", r.readBytes, r.seeks)
		})
	}
}

func TestProbeTarCollectionTruncatedUnrelatedBody(t *testing.T) {
	archive := discoveryTestTAR(t, "ignored.txt", bytes.Repeat([]byte("x"), 1024))
	_, err := probeTarCollection(context.Background(), bytes.NewReader(archive[:600]), "renamed.tar")
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated body accepted or wrong error: %v", err)
	}
}

func TestCollectionDiscoveryCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &discoveryCountReader{ReadSeeker: bytes.NewReader(discoveryTestTAR(t, "2A3_0001.bin", []byte("chunk")))}
	if _, err := probeTarCollection(ctx, r, "renamed.tar"); !errors.Is(err, context.Canceled) || r.readBytes != 0 {
		t.Fatalf("canceled probe accessed archive: read=%d, err=%v", r.readBytes, err)
	}
	if _, _, err := FindCollections(ctx, filepath.Join(t.TempDir(), "missing")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled discovery accessed directory: %v", err)
	}
}
