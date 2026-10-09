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

func TestIndexTARSkipsLargeBodies(t *testing.T) {
	const bodySize int64 = 8 << 30
	for _, name := range []string{"ignored.dat", "2A2_0002.bin"} {
		t.Run(name, func(t *testing.T) {
			var header bytes.Buffer
			w := tar.NewWriter(&header)
			if err := w.WriteHeader(&tar.Header{Name: name, Size: bodySize, Mode: 0600, Format: tar.FormatGNU}); err != nil {
				t.Fatal(err)
			}
			archive := discoveryLargeTAR{header: header.Bytes(), tailOffset: int64(header.Len()) + bodySize,
				tail: discoveryTestTAR(t, "2A2_0001.bin", []byte("one"))}
			source := &discoveryCountReader{ReadSeeker: io.NewSectionReader(archive, 0, archive.tailOffset+int64(len(archive.tail)))}
			chunks, err := indexTarChunks(context.Background(), source, "test.tar")
			if err != nil || len(chunks) == 0 {
				t.Fatalf("large ordinary entry: %v, %v", chunks, err)
			}
			if chunks[0].name != "2A2_0001.bin" || chunks[0].offset != archive.tailOffset+512 || chunks[0].size != 3 {
				t.Fatalf("wrong order or overflowed offset: %+v", chunks)
			}
			wantCount := 1
			if name == "2A2_0002.bin" {
				wantCount = 2
				if chunks[1].size != bodySize || chunks[1].offset != 512 {
					t.Fatalf("wrong large chunk location: %+v", chunks[1])
				}
			}
			if len(chunks) != wantCount || source.readBytes > 4096 || source.seeks == 0 {
				t.Fatalf("index consumed body or lost chunks: count=%d, read=%d, seeks=%d", len(chunks), source.readBytes, source.seeks)
			}
		})
	}
}

func TestIndexedTARKeepsDuplicateMembersAndLongNames(t *testing.T) {
	for _, format := range []tar.Format{tar.FormatPAX, tar.FormatGNU} {
		t.Run(fmt.Sprint(format), func(t *testing.T) {
			var archive bytes.Buffer
			w := tar.NewWriter(&archive)
			prefix := strings.Repeat("nested/", 30)
			for i, name := range []string{"2A2_10000.bin", "2A2_9999.bin", "2A2_9999.bin", "2A2_0001.bin"} {
				if err := w.WriteHeader(&tar.Header{Name: prefix + name, Mode: 0600, Size: 1, Format: format}); err != nil {
					t.Fatal(err)
				}
				if _, err := w.Write([]byte{byte(i)}); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			input := filepath.Join(t.TempDir(), "renamed.tar")
			if err := os.WriteFile(input, archive.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			reader := NewCollectionReader(Collection{Name: "2A2", Path: input, Format: FormatBin})
			defer reader.Close()
			for _, want := range []byte{3, 1, 2, 0} {
				if got, err := reader.ReadNextChunk(context.Background()); err != nil || !bytes.Equal(got, []byte{want}) {
					t.Fatalf("lost duplicate or wrong offset/order: %v, %v; want %d", got, err, want)
				}
			}
			if _, err := reader.ReadNextChunk(context.Background()); err != io.EOF {
				t.Fatalf("end of archive: %v", err)
			}
		})
	}
}

func TestIndexedTARRejectsNonRegularChunks(t *testing.T) {
	for _, kind := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeDir, tar.TypeFifo} {
		t.Run(fmt.Sprintf("%c", kind), func(t *testing.T) {
			var archive bytes.Buffer
			w := tar.NewWriter(&archive)
			if err := w.WriteHeader(&tar.Header{Name: "2A2_0001.bin", Typeflag: kind, Linkname: "elsewhere", Mode: 0600}); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			_, err := indexTarChunks(context.Background(), bytes.NewReader(archive.Bytes()), "test.tar")
			if err == nil || !strings.Contains(err.Error(), "not a regular file") || !strings.Contains(err.Error(), "2A2_0001.bin") {
				t.Fatalf("non-regular chunk was skipped/accepted: %v", err)
			}
		})
	}
}

// A seekable source that fails while indexing reads the last byte of a body.
type indexReadFailure struct {
	*bytes.Reader
	failAt     int64
	withBytes  bool
	failure    error
	failed     bool
	afterError int
}

func (r *indexReadFailure) Read(p []byte) (int, error) {
	if r.failed {
		r.afterError++
	}
	pos, _ := r.Reader.Seek(0, io.SeekCurrent)
	if !r.failed && pos == r.failAt {
		r.failed = true
		if !r.withBytes {
			return 0, r.failure
		}
		n, _ := r.Reader.Read(p)
		return n, r.failure
	}
	return r.Reader.Read(p)
}

func TestIndexTARPreservesBodyReadFailure(t *testing.T) {
	for _, member := range []string{"2A2_0001.bin", "._2A2_0001.bin", "ignored.txt"} {
		for _, withBytes := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/withBytes=%t", member, withBytes), func(t *testing.T) {
				failure := &os.PathError{Op: "read", Path: "test.tar", Err: errors.New("injected storage failure")}
				r := &indexReadFailure{Reader: bytes.NewReader(discoveryTestTAR(t, member, []byte("payload"))),
					failAt: 512 + 6, withBytes: withBytes, failure: failure}
				chunks, err := indexTarChunks(context.Background(), r, "test.tar")
				if !errors.Is(err, failure) || len(chunks) != 0 || !strings.Contains(err.Error(), member) || strings.Contains(err.Error(), "read TAR header") {
					t.Fatalf("body error lost/misreported: chunks=%v, err=%v", chunks, err)
				}
				if !r.failed || r.afterError != 0 {
					t.Fatalf("failure not exercised or reading continued: failed=%t, after=%d", r.failed, r.afterError)
				}
			})
		}
	}
}

func TestIndexedTARTruncatedAfterIndexing(t *testing.T) {
	first := discoveryTestTAR(t, "2A2_0001.bin", []byte("one"))
	archive := append(first[:len(first)-1024], discoveryTestTAR(t, "2A2_0002.bin", []byte("two"))...)
	input := filepath.Join(t.TempDir(), "2A2.tar")
	if err := os.WriteFile(input, archive, 0600); err != nil {
		t.Fatal(err)
	}
	reader := NewCollectionReader(Collection{Name: "2A2", Path: input, Format: FormatBin})
	defer reader.Close()
	if _, err := reader.ReadNextChunk(context.Background()); err != nil {
		t.Fatal(err)
	}
	opened := reader.tarFile
	if err := os.Truncate(input, 1537); err != nil {
		t.Fatal(err)
	}
	data, err := reader.ReadNextChunk(context.Background())
	if !errors.Is(err, io.ErrUnexpectedEOF) || len(data) != 0 || !strings.Contains(err.Error(), "2A2_0002.bin") || !strings.Contains(err.Error(), input) {
		t.Fatalf("truncated indexed body accepted: %q, %v", data, err)
	}
	if _, err := opened.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("failure left file open: %v", err)
	}
	if _, next := reader.ReadNextChunk(context.Background()); next != err {
		t.Fatalf("failure not terminal: %v, want %v", next, err)
	}
}

func TestTARNonSeekableStoredOrder(t *testing.T) {
	second := discoveryTestTAR(t, "2A2_0002.bin", []byte("two"))
	archive := append(second[:len(second)-1024], discoveryTestTAR(t, "2A2_0001.bin", []byte("one"))...)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// The fixture fits in a pipe, but write concurrently to avoid relying on its
	// capacity on any particular OS. The fallback consumes stored order.
	done := make(chan error, 1)
	go func() {
		_, writeErr := w.Write(archive)
		done <- errors.Join(writeErr, w.Close())
	}()
	reader := NewCollectionReader(Collection{Name: "2A2", Path: "pipe.tar", Format: FormatBin})
	reader.tarFile, reader.tarReader = r, tar.NewReader(r)
	defer reader.Close()
	for _, want := range []string{"two", "one"} {
		if got, err := reader.ReadNextChunk(context.Background()); err != nil || string(got) != want {
			t.Fatalf("stored-order fallback: got %q, %v; want %q", got, err, want)
		}
	}
	if _, err := reader.ReadNextChunk(context.Background()); err != io.EOF {
		t.Fatalf("end of non-seekable stream: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestIndexTARCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := &discoveryCountReader{ReadSeeker: bytes.NewReader(nil)}
	if _, err := indexTarChunks(ctx, source, "test.tar"); !errors.Is(err, context.Canceled) || source.seeks != 0 || source.readBytes != 0 {
		t.Fatalf("canceled index read archive: seeks=%d, read=%d, err=%v", source.seeks, source.readBytes, err)
	}
}
