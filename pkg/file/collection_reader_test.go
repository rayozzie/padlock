// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCollectionReaderCloseBeforeEOF(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "2A2")
	for chunk := 1; chunk <= 2; chunk++ {
		if err := WriteNamedChunk(ctx, GetFormatter(FormatBin), dir, "2A2", chunk, []byte("encoded chunk")); err != nil {
			t.Fatal(err)
		}
	}
	archive, err := TarCollection(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	reader := NewCollectionReader(Collection{Name: "2A2", Path: archive, Format: FormatBin})
	defer reader.Close()
	if _, err := reader.ReadNextChunk(ctx); err != nil {
		t.Fatal(err)
	}
	opened := reader.tarFile
	if opened == nil {
		t.Fatal("fixture did not open the TAR stream")
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := opened.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("early close left the TAR file open: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("repeated close failed: %v", err)
	}
}

func TestCollectionReaderChunkExtensions(t *testing.T) {
	ctx := context.Background()
	for _, archive := range []bool{false, true} {
		for _, tc := range []struct {
			format Format
			ext    string
		}{
			{FormatBin, ".bin"}, {FormatBin, ".BIN"}, {FormatBin, ".BiN"},
			{FormatPNG, ".PNG"}, {FormatPNG, ".png"}, {FormatPNG, ".PnG"},
		} {
			t.Run(fmt.Sprintf("%s/tar=%t", tc.ext, archive), func(t *testing.T) {
				base := t.TempDir()
				dir := filepath.Join(base, "2A2")
				chunks := [][]byte{[]byte("first chunk"), {0, 1, 127, 128, 255}}
				for i, data := range chunks {
					if err := WriteNamedChunk(ctx, GetFormatter(tc.format), dir, "2A2", i+1, data); err != nil {
						t.Fatal(err)
					}
					name := fmt.Sprintf("2A2_%04d.bin", i+1)
					if tc.format == FormatPNG {
						name = fmt.Sprintf("IMG2A2_%04d.PNG", i+1)
					}
					renamed := name[:len(name)-len(filepath.Ext(name))] + tc.ext
					if name != renamed {
						if err := os.Rename(filepath.Join(dir, name), filepath.Join(dir, renamed)); err != nil {
							t.Fatal(err)
						}
					}
				}
				if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a chunk"), 0600); err != nil {
					t.Fatal(err)
				}
				if archive {
					if _, err := TarCollection(ctx, dir); err != nil {
						t.Fatal(err)
					}
					if err := os.RemoveAll(dir); err != nil {
						t.Fatal(err)
					}
				}
				collections, tempDir, err := FindCollections(ctx, base)
				if tempDir != "" {
					t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
				}
				if err != nil || len(collections) != 1 {
					t.Fatalf("expected one discovered collection: %v, err=%v", collections, err)
				}
				if collections[0].Format != tc.format {
					t.Fatalf("detected format %q, want %q", collections[0].Format, tc.format)
				}
				for _, format := range []Format{tc.format, ""} {
					collection := collections[0]
					collection.Format = format
					reader := NewCollectionReader(collection)
					t.Cleanup(func() {
						if reader.tarFile != nil {
							_ = reader.tarFile.Close()
						}
					})
					for i, want := range chunks {
						got, err := reader.ReadNextChunk(ctx)
						if err != nil || !bytes.Equal(got, want) {
							t.Fatalf("format=%q chunk=%d: got %q, err=%v; want %q", format, i+1, got, err, want)
						}
					}
					if _, err := reader.ReadNextChunk(ctx); err != io.EOF {
						t.Fatalf("expected EOF after both chunks, got %v", err)
					}
				}
			})
		}
	}
}
