// Copyright 2025 Ray Ozzie. All rights reserved.

package padlock

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rayozzie/padlock/internal/testarchive"
	"github.com/rayozzie/padlock/pkg/file"
	"github.com/rayozzie/padlock/pkg/pad"
)

func decodeErrorArchive(t *testing.T, kind string) []byte {
	t.Helper()
	// Keep more incompressible bytes after the failure than gzip can read ahead,
	// so the decoder is still writing when archive consumption fails.
	tail := make([]byte, 128*1024)
	_, _ = rand.New(rand.NewSource(4)).Read(tail)
	switch kind {
	case "sparse":
		return append(testarchive.Sparse("hole.bin", 1<<20, "pax1.0"), tail...)
	case "tar_header":
		return append(bytes.Repeat([]byte{0xff}, 512), tail...)
	case "gzip_header":
		return append([]byte{0x1f, 0x8b, 0xff, 0, 0, 0, 0, 0, 0, 0}, tail...)
	case "file_exists":
		var archive bytes.Buffer
		w := tar.NewWriter(&archive)
		for _, entry := range []struct {
			name string
			data []byte
		}{
			{"duplicate.txt", []byte("first contents")},
			{"duplicate.txt", []byte("must not overwrite the first file")},
			{"unreached.bin", tail},
		} {
			if err := w.WriteHeader(&tar.Header{Name: entry.name, Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(entry.data))}); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write(entry.data); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		return archive.Bytes()
	default:
		t.Fatalf("unknown fixture %q", kind)
		return nil
	}
}

func encodeDecodeErrorFixture(t *testing.T, data []byte, format Format, archive bool) string {
	t.Helper()
	ctx := context.Background()
	encoded := t.TempDir()
	encoder, err := pad.NewPadForEncode(ctx, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	err = encoder.Encode(ctx, 1<<20, bytes.NewReader(data), pad.NewCryptoRand(),
		func(name string, chunk int, _ string) (io.WriteCloser, error) {
			return file.NewChunkWriter(ctx, file.GetFormatter(format), filepath.Join(encoded, name), 0, chunk), nil
		}, string(format))
	if err != nil {
		t.Fatal(err)
	}
	if archive {
		for _, name := range []string{"2A2", "2B2"} {
			dir := filepath.Join(encoded, name)
			if _, err := file.TarCollection(ctx, dir); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
		}
	}
	return encoded
}

func TestDecodeDirectoryPreservesExtractionCause(t *testing.T) {
	for _, failure := range []struct {
		kind string
		want error
	}{
		{"sparse", file.ErrSparseTarEntry},
		{"tar_header", tar.ErrHeader},
		{"gzip_header", gzip.ErrHeader},
		{"file_exists", os.ErrExist},
	} {
		for _, format := range []Format{FormatBin, FormatPNG} {
			for _, archive := range []bool{false, true} {
				for _, compressed := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/tar=%t/gzip=%t", failure.kind, format, archive, compressed), func(t *testing.T) {
						data := decodeErrorArchive(t, failure.kind)
						if compressed {
							var buf bytes.Buffer
							w := gzip.NewWriter(&buf)
							if _, err := w.Write(data); err != nil {
								t.Fatal(err)
							}
							if err := w.Close(); err != nil {
								t.Fatal(err)
							}
							data = buf.Bytes()
						}
						encoded := encodeDecodeErrorFixture(t, data, format, archive)
						for _, dryRun := range []bool{false, true} {
							if dryRun && failure.kind == "file_exists" {
								continue // Dry runs do not create filesystem entries.
							}
							t.Run(fmt.Sprintf("dry=%t", dryRun), func(t *testing.T) {
								output := filepath.Join(t.TempDir(), "restored")
								err := DecodeDirectory(context.Background(), DecodeConfig{
									InputDir: encoded, OutputDir: output, SizeOnly: dryRun, Compression: CompressionGzip,
								})
								if !errors.Is(err, failure.want) {
									t.Errorf("returned error lost extraction cause %v: %v", failure.want, err)
								}
								if !errors.Is(err, io.ErrClosedPipe) {
									t.Errorf("fixture must also preserve the interrupted decoder's pipe error: %v", err)
								}
								if failure.kind == "file_exists" {
									var pathErr *os.PathError
									if !errors.As(err, &pathErr) || filepath.Base(pathErr.Path) != "duplicate.txt" {
										t.Errorf("returned error lost filesystem operation/path: %v", err)
									}
									contents, readErr := os.ReadFile(filepath.Join(output, "duplicate.txt"))
									if readErr != nil || string(contents) != "first contents" {
										t.Errorf("duplicate entry changed the first file: %q, %v", contents, readErr)
									}
								}
								if dryRun {
									if _, err := os.Stat(output); !os.IsNotExist(err) {
										t.Errorf("failed dry run created output: %v", err)
									}
								}
							})
						}
					})
				}
			}
		}
	}
}

func TestDecodeDirectoryPreservesUnexpectedEOF(t *testing.T) {
	encoded := encodeDecodeErrorFixture(t, bytes.Repeat([]byte("x"), 1024), FormatBin, false)
	chunk := filepath.Join(encoded, "2A2", "2A2_0001.bin")
	info, err := os.Stat(chunk)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(chunk, info.Size()-1); err != nil {
		t.Fatal(err)
	}
	err = DecodeDirectory(context.Background(), DecodeConfig{InputDir: encoded, OutputDir: filepath.Join(t.TempDir(), "restored")})
	if !errors.Is(err, io.ErrUnexpectedEOF) || !strings.Contains(err.Error(), "unexpected EOF") {
		t.Fatalf("returned error lost collection truncation: %v", err)
	}
}

func TestDecodeDirectoryPreservesLateGzipFailure(t *testing.T) {
	var compressed bytes.Buffer
	w := gzip.NewWriter(&compressed)
	if _, err := w.Write(make([]byte, 1024)); err != nil { // Complete, empty TAR.
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data := compressed.Bytes()
	data[len(data)-8] ^= 1 // Damage the CRC without changing the TAR bytes.
	encoded := encodeDecodeErrorFixture(t, data, FormatBin, false)
	for _, dryRun := range []bool{false, true} {
		t.Run(fmt.Sprintf("dry=%t", dryRun), func(t *testing.T) {
			err := DecodeDirectory(context.Background(), DecodeConfig{
				InputDir: encoded, OutputDir: filepath.Join(t.TempDir(), "restored"),
				SizeOnly: dryRun, Compression: CompressionGzip,
			})
			if !errors.Is(err, gzip.ErrChecksum) || errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("expected gzip footer failure after reconstruction finished, got %v", err)
			}
		})
	}
}
