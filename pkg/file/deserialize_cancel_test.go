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

	"github.com/rayozzie/padlock/pkg/trace"
)

type cancelOnWrite struct {
	cancel context.CancelFunc
	bytes  int
}

func (w *cancelOnWrite) Write(p []byte) (int, error) {
	w.bytes += len(p)
	w.cancel()
	return len(p), nil
}

func TestDirectoryArchiveCancellationStopsBufferedData(t *testing.T) {
	const bodySize = 128 * 1024
	archive := directoryModeTar(t,
		extractionTestEntry{header: tar.Header{Name: "first", Typeflag: tar.TypeReg, Mode: 0600}, body: string(bytes.Repeat([]byte("x"), bodySize))},
		extractionTestEntry{header: tar.Header{Name: "second", Typeflag: tar.TypeReg, Mode: 0600}, body: "must not be processed"},
	)
	for _, gzip := range []bool{false, true} {
		for _, duringBody := range []bool{false, true} {
			t.Run(fmt.Sprintf("gzip=%t/during_body=%t", gzip, duringBody), func(t *testing.T) {
				data := archive
				if gzip {
					data = extractionTestGzip(t, data)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				entries := 0
				writer := &cancelOnWrite{cancel: cancel}
				err := processDirectoryTar(ctx, bytes.NewReader(data), func(_ *tar.Header, r io.Reader) (int64, error) {
					entries++
					if duringBody {
						return io.Copy(writer, r)
					}
					n, err := io.Copy(io.Discard, r)
					cancel()
					return n, err
				}, trace.FromContext(ctx))
				if !errors.Is(err, context.Canceled) || entries != 1 {
					t.Errorf("archive processed %d entries and returned %v after cancellation", entries, err)
				}
				if duringBody && (writer.bytes == 0 || writer.bytes >= bodySize) {
					t.Errorf("cancellation did not interrupt buffered body: wrote %d of %d bytes", writer.bytes, bodySize)
				}
			})
		}
	}
}

func TestCanceledDeserializeRestoresDirectoryModes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	archive := directoryModeTar(t,
		extractionTestEntry{header: tar.Header{Name: "locked", Typeflag: tar.TypeDir, Mode: 0555}},
		extractionTestEntry{header: tar.Header{Name: "locked/first", Typeflag: tar.TypeReg, Mode: 0600}, body: "restored before cancellation"},
		extractionTestEntry{header: tar.Header{Name: "locked/second", Typeflag: tar.TypeReg, Mode: 0600}, body: "must not be restored"},
	)
	dest := filepath.Join(t.TempDir(), "restored")
	unlockDirectoriesAfterTest(t, dest, "locked")
	// Cancel immediately before the second file's header is read. The first
	// file and directory exist, and deferred permissions must still be applied.
	const firstFileEnd = 3 * 512 // directory header, file header, padded body
	r := io.MultiReader(bytes.NewReader(archive[:firstFileEnd]), &directoryMutationReader{
		Reader: bytes.NewReader(archive[firstFileEnd:]),
		mutate: func() error { cancel(); return nil },
	})
	err := DeserializeDirectoryFromStream(ctx, dest, r, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled extraction returned %v", err)
	}
	assertExtractionFile(t, filepath.Join(dest, "locked", "first"), "restored before cancellation")
	if _, err := os.Stat(filepath.Join(dest, "locked", "second")); !os.IsNotExist(err) {
		t.Errorf("extracted a later file after cancellation: %v", err)
	}
	assertOutputPermissions(t, filepath.Join(dest, "locked"), 0555)
}

func TestAlreadyCanceledDirectoryArchiveDoesNotClear(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	archive := bytes.NewReader(make([]byte, 1024))
	dest := t.TempDir()
	marker := filepath.Join(dest, "keep")
	if err := os.WriteFile(marker, []byte("previous contents"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := DeserializeDirectoryFromStream(ctx, dest, archive, true); !errors.Is(err, context.Canceled) {
		t.Errorf("restore returned %v", err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "previous contents" {
		t.Error("canceled extraction cleared existing output")
	}
	if err := ValidateDirectoryStream(ctx, archive); !errors.Is(err, context.Canceled) {
		t.Errorf("dry run returned %v", err)
	}
}
