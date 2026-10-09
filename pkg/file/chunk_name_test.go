// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestParseChunkFilename(t *testing.T) {
	for _, tc := range []struct {
		name, collection string
		format           Format
	}{
		{"2A3_0001.bin", "2A3", FormatBin},
		{"IMG2A3_0001.PNG", "2A3", FormatPNG},
		{"2a3_1.BiN", "2a3", FormatBin},
		{"IMG12Z26_18446744073709551616.pNg", "12Z26", FormatPNG},
		{"2A3_0.bin", "2A3", FormatBin}, // Let the decoder reject invalid numbers.
		{"2A3_01.png", "2A3", FormatPNG},
		{"IMG2A3_01.bin", "2A3", FormatBin},
		{"IMG2A3_0001 (1).PNG", "2A3", FormatPNG},
		{"img2a3_0001 (conflicted copy_2026-10-09).png", "2a3", FormatPNG},
		{"ImG2A3_1.png", "2A3", FormatPNG},
		{"2A3_1x.bin", "2A3", FormatBin},
		{"2A3_1_2.bin", "2A3", FormatBin},
	} {
		if name, format := ParseChunkFilename(tc.name); name != tc.collection || format != tc.format {
			t.Errorf("%s: got %q, %q", tc.name, name, format)
		}
	}
	for _, name := range []string{
		"._2A3_0001.bin", "._IMG2A3_0001.PNG", "notes.bin", "photo.png",
		"2A3.bin", "2A3_.bin", "2A3_+1.bin", "2A3_-1.bin",
		"2A3_１.bin", "2A3_1.bin.bak", "12A_1.bin", "copy-IMG2A3_1.png",
		"a/2A3_1.bin", "a\\2A3_1.bin",
	} {
		if collection, format := ParseChunkFilename(name); collection != "" || format != "" {
			t.Errorf("non-chunk %q identified as %q, %q", name, collection, format)
		}
	}
}

func TestCollectionSelectionIgnoresSidecars(t *testing.T) {
	ctx := context.Background()
	for _, format := range []Format{FormatBin, FormatPNG} {
		t.Run(string(format), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "2A3")
			payload := []byte("the real chunk")
			formatter := GetFormatter(format)
			if err := WriteNamedChunk(ctx, formatter, dir, "2A3", 1, payload); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("chunk fixture: %v, %v", entries, err)
			}
			for _, name := range []string{"._" + entries[0].Name(), "._2A3_1.bin", "0001.bin", "photo.png"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("not a chunk"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := DetermineCollectionFormat(dir); got != format || err != nil {
				t.Fatalf("sidecars changed format: %q, %v", got, err)
			}
			if got, err := formatter.ReadChunk(ctx, dir, 0, 1); err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("direct formatter read sidecar: %q, %v", got, err)
			}
			reader := NewCollectionReader(Collection{Name: "2A3", Path: dir, Format: format})
			defer reader.Close()
			if got, err := reader.ReadNextChunk(ctx); err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("collection read sidecar: %q, %v", got, err)
			}
			if _, err := reader.ReadNextChunk(ctx); err != io.EOF {
				t.Fatalf("unrelated file was selected: %v", err)
			}
			if err := os.Remove(filepath.Join(dir, entries[0].Name())); err != nil {
				t.Fatal(err)
			}
			if _, err := DetermineCollectionFormat(dir); err == nil {
				t.Fatal("discovered a collection from only metadata/unrelated files")
			}
		})
	}
}

func TestCollectionSelectionKeepsEveryMatchingCandidate(t *testing.T) {
	ctx := context.Background()
	for _, archive := range []bool{false, true} {
		t.Run(fmt.Sprintf("tar=%t", archive), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "2A3")
			// Distinct spellings of the same number, different labels/formats,
			// and overflowing suffixes must reach the decoder, never disappear.
			names := []string{"2A3_0001.bin", "2A3_1.BIN", "IMG2B3_1.PNG", "2A3_18446744073709551616.bin"}
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range names {
				body := []byte(name)
				if _, format := ParseChunkFilename(name); format == FormatPNG {
					var png bytes.Buffer
					if err := encodePNGWithData(&png, createSmallPNG(), body); err != nil {
						t.Fatal(err)
					}
					body = png.Bytes()
				}
				if err := os.WriteFile(filepath.Join(dir, name), body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			input := dir
			if archive {
				var err error
				input, err = TarCollection(ctx, dir)
				if err != nil {
					t.Fatal(err)
				}
			}
			reader := NewCollectionReader(Collection{Name: "2A3", Path: input, Format: FormatBin})
			defer reader.Close()
			for _, name := range names {
				if got, err := reader.ReadNextChunk(ctx); err != nil || string(got) != name {
					t.Fatalf("candidate %s lost: got %q, %v", name, got, err)
				}
			}
			if _, err := reader.ReadNextChunk(ctx); err != io.EOF {
				t.Fatalf("want EOF, got %v", err)
			}
		})
	}
}

func TestCollectionDiscoverySkipsAppleDoubleArchive(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"2A2.tar", "._2A2.tar"} {
		// Even a sidecar that is itself a valid TAR is excluded by name.
		if err := os.WriteFile(filepath.Join(root, name), discoveryTestTAR(t, "2A2_0001.bin", []byte("chunk")), 0600); err != nil {
			t.Fatal(err)
		}
	}
	collections, _, err := FindCollections(context.Background(), root)
	if err != nil || len(collections) != 1 || collections[0].Path != filepath.Join(root, "2A2.tar") {
		t.Fatalf("sidecar discovered as collection: %+v, %v", collections, err)
	}
}
