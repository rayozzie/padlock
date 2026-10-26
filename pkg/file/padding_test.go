// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"testing/iotest"
)

func TestGzipStoredZerosExactSizes(t *testing.T) {
	for _, size := range []int64{23, 24, 25, 26, 100, 65557, 65558, 65559, 65560, 65565, 65566, 131097, 131098, 131099, 200040, 1 << 20, 1<<20 + 1} {
		data, err := io.ReadAll(iotest.OneByteReader(gzipStoredZeros(size)))
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(data)) != size {
			t.Fatalf("size %d: produced %d bytes", size, len(data))
		}
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		zr.Multistream(false)
		out, err := io.ReadAll(zr)
		if err != nil {
			t.Fatalf("size %d: decompress: %v", size, err)
		}
		if bytes.ContainsFunc(out, func(r rune) bool { return r != 0 }) {
			t.Fatalf("size %d: nonzero padding payload", size)
		}
		if int64(len(out)) > size-gzipStoredMinBytes {
			t.Fatalf("size %d: payload %d exceeds framing budget", size, len(out))
		}
	}
	if data, _ := io.ReadAll(gzipStoredZeros(1)); len(data) != gzipStoredMinBytes {
		t.Fatalf("undersized request produced %d bytes, want %d", len(data), gzipStoredMinBytes)
	}
}

func paddingArchive(t *testing.T, gzipped bool) ([]byte, map[string][]byte) {
	t.Helper()
	files := map[string][]byte{
		"a.txt":        bytes.Repeat([]byte("alpha\n"), 300),
		"dir/b.bin":    bytes.Repeat([]byte{1, 2, 3, 4, 5, 6, 7}, 777),
		"dir/empty.md": nil,
	}
	var archive bytes.Buffer
	var sink io.Writer = &archive
	var zw *gzip.Writer
	if gzipped {
		zw = gzip.NewWriter(&archive)
		sink = zw
	}
	tw := tar.NewWriter(sink)
	for _, name := range []string{"a.txt", "dir/b.bin", "dir/empty.md"} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(files[name]))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(files[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if zw != nil {
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return archive.Bytes(), files
}

func TestChunkPadderAlignsAndRestores(t *testing.T) {
	ctx := context.Background()
	for _, gzipped := range []bool{true, false} {
		archive, files := paddingArchive(t, gzipped)
		for _, chunk := range []int{1, 7, 22, 23, 24, 100, 4096, 1 << 16, 1 << 20, len(archive), len(archive) + 5} {
			for _, small := range []bool{false, true} {
				t.Run(fmt.Sprintf("gzip=%t/chunk=%d/small=%t", gzipped, chunk, small), func(t *testing.T) {
					var source io.Reader = bytes.NewReader(archive)
					if small {
						source = iotest.OneByteReader(source)
					}
					padder := NewChunkPadder(source, chunk, gzipped)
					padded, err := io.ReadAll(padder)
					if err != nil {
						t.Fatal(err)
					}
					if len(padded)%chunk != 0 {
						t.Fatalf("padded length %d is not a multiple of %d", len(padded), chunk)
					}
					if !bytes.HasPrefix(padded, archive) {
						t.Fatal("padding altered the archive bytes")
					}
					if got := int64(len(padded) - len(archive)); got != padder.Padding() {
						t.Fatalf("Padding() reported %d, appended %d", padder.Padding(), got)
					}
					if padder.Padding() >= int64(chunk)+gzipStoredMinBytes && len(archive)%chunk != 0 {
						t.Fatalf("appended %d bytes; more than one extra chunk", padder.Padding())
					}
					if err := ValidateDirectoryStream(ctx, bytes.NewReader(padded)); err != nil {
						t.Fatalf("validation rejected padded stream: %v", err)
					}
					out := t.TempDir()
					if err := DeserializeDirectoryFromStream(ctx, out, bytes.NewReader(padded), false); err != nil {
						t.Fatalf("restore rejected padded stream: %v", err)
					}
					restored := map[string][]byte{}
					err = filepath.Walk(out, func(path string, info os.FileInfo, err error) error {
						if err != nil || info.IsDir() {
							return err
						}
						rel, _ := filepath.Rel(out, path)
						data, err := os.ReadFile(path)
						restored[filepath.ToSlash(rel)] = data
						return err
					})
					if err != nil {
						t.Fatal(err)
					}
					if len(restored) != len(files) {
						t.Fatalf("restored %d files, want %d: %v", len(restored), len(files), restored)
					}
					for name, want := range files {
						if !bytes.Equal(restored[name], want) {
							t.Fatalf("restored %s differs", name)
						}
					}
				})
			}
		}
	}
}

func TestChunkPadderAlignedInputAndPassthrough(t *testing.T) {
	archive, _ := paddingArchive(t, true)
	padder := NewChunkPadder(bytes.NewReader(archive), len(archive), true)
	if padded, err := io.ReadAll(padder); err != nil || !bytes.Equal(padded, archive) || padder.Padding() != 0 {
		t.Fatalf("aligned input was changed: err=%v padding=%d", err, padder.Padding())
	}
	for _, chunk := range []int{0, -1} {
		padder := NewChunkPadder(bytes.NewReader(archive), chunk, true)
		if padded, err := io.ReadAll(padder); err != nil || !bytes.Equal(padded, archive) || padder.Padding() != 0 {
			t.Fatalf("chunk %d: passthrough changed the stream: err=%v", chunk, err)
		}
	}
	// A tiny shortfall cannot hold a gzip member, so a whole extra chunk is added.
	chunk := len(archive) + 5
	padder = NewChunkPadder(bytes.NewReader(archive), chunk, true)
	padded, err := io.ReadAll(padder)
	if err != nil || len(padded) != 2*chunk || padder.Padding() != int64(chunk+5) {
		t.Fatalf("tiny shortfall: len=%d padding=%d err=%v, want %d and %d", len(padded), padder.Padding(), err, 2*chunk, chunk+5)
	}
	// Uncompressed streams are padded with zero bytes only.
	plain, _ := paddingArchive(t, false)
	padder = NewChunkPadder(bytes.NewReader(plain), len(plain)+9, false)
	padded, err = io.ReadAll(padder)
	if err != nil || len(padded) != len(plain)+9 || bytes.ContainsFunc(padded[len(plain):], func(r rune) bool { return r != 0 }) {
		t.Fatalf("uncompressed padding is wrong: len=%d err=%v", len(padded), err)
	}
}

func TestChunkPadderPropagatesSourceErrors(t *testing.T) {
	archive, _ := paddingArchive(t, true)
	failure := errors.New("source failed")
	source := io.MultiReader(bytes.NewReader(archive[:len(archive)/2]), iotest.ErrReader(failure))
	padder := NewChunkPadder(source, 4096, true)
	data, err := io.ReadAll(padder)
	if !errors.Is(err, failure) {
		t.Fatalf("got %v, want the source error", err)
	}
	if len(data) != len(archive)/2 || padder.Padding() != 0 {
		t.Fatalf("a failed source was padded: %d bytes, padding %d", len(data), padder.Padding())
	}
}
