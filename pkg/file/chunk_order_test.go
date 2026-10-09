// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCollectionChunkOrderAcrossDigitBoundaries(t *testing.T) {
	ctx := context.Background()
	for _, format := range []Format{FormatBin, FormatPNG} {
		for _, storage := range []string{"loose", "repacked-tar", "in-place-tar", "extracted-tar"} {
			t.Run(fmt.Sprintf("%s/%s", format, storage), func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "2A2")
				// A small fixture isolates every relevant digit boundary. The
				// padlock integration test also restores a complete 10,000+ chunk backup.
				chunks := []int{1, 999, 1000, 1001, 9999, 10000, 10001, 99999, 100000}
				for i, n := range chunks {
					if err := WriteNamedChunk(ctx, GetFormatter(format), dir, "2A2", n, []byte(fmt.Sprint(n))); err != nil {
						t.Fatal(err)
					}
					name := fmt.Sprintf("2A2_%04d.bin", n)
					if format == FormatPNG {
						name = fmt.Sprintf("IMG2A2_%04d.PNG", n)
					}
					// Extension case must not affect numeric ordering.
					if i%2 == 0 {
						ext := filepath.Ext(name)
						mixedExt := ".BiN"
						if format == FormatPNG {
							mixedExt = ".PnG"
						}
						renamed := strings.TrimSuffix(name, ext) + mixedExt
						if err := os.Rename(filepath.Join(dir, name), filepath.Join(dir, renamed)); err != nil {
							t.Fatal(err)
						}
					}
				}
				if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0600); err != nil {
					t.Fatal(err)
				}
				path := dir
				if storage != "loose" {
					var archive string
					var err error
					if storage == "in-place-tar" {
						archive, err = TarDirectoryContents(ctx, dir, "2A2")
					} else {
						archive, err = TarCollection(ctx, dir)
					}
					if err != nil {
						t.Fatal(err)
					}
					path = archive
					if storage == "extracted-tar" {
						path, err = ExtractTarCollection(ctx, archive, t.TempDir())
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				for _, detectedFormat := range []Format{format, ""} {
					reader := NewCollectionReader(Collection{Name: "2A2", Path: path, Format: detectedFormat})
					t.Cleanup(func() { _ = reader.Close() })
					for _, n := range chunks {
						got, err := reader.ReadNextChunk(ctx)
						if err != nil || string(got) != fmt.Sprint(n) {
							t.Fatalf("format=%q: want chunk %d, got %q, err=%v", detectedFormat, n, got, err)
						}
					}
					if _, err := reader.ReadNextChunk(ctx); err != io.EOF {
						t.Fatalf("want EOF after all chunks, got %v", err)
					}
				}
			})
		}
	}
}

func TestCollectionChunkOrderKeepsAllCandidates(t *testing.T) {
	// Prefixes and zero-padding do not change the numeric value. Ties use
	// filename order and retain both candidates for the decoder to validate.
	// Very large suffixes must not overflow on either 32- or 64-bit systems.
	want := []string{
		"zero_0000.bin",
		"z_00001.bin",
		"a_0002.bin",
		"b_2.BIN",
		"a_0000000010.bin",
		"x_9999.bin",
		"x_10000.bin",
		"x_18446744073709551615.bin",
		"x_18446744073709551616.bin",
		"x_100000000000000000000.bin",
		"0001.bin",
		"first.bin",
		"invalid_+1.bin",
		"invalid_-1.bin",
		"invalid_.bin",
		"invalid_1x.bin",
		"invalid_１.bin",
		"last.bin",
	}
	// The sorter also serves general directory-to-TAR helpers. Collection
	// readers now select actual chunk names before invoking it.
	got := slices.Clone(want)
	slices.Reverse(got)
	sortChunkFiles(got)
	if !slices.Equal(got, want) {
		t.Fatalf("sorted candidates = %q, want %q", got, want)
	}
}

func TestDecoratedChunkNumberOrder(t *testing.T) {
	want := []string{
		"2A2_0001 (copy_99999).bin",
		"IMG2a2_1 (1).PNG", // Duplicate number is retained.
		"img2a2_0002 (conflicted_100000).png",
		"2A2_9999 (9).bin",
		"2A2_10000 (1).bin",
		"2A2_18446744073709551615 (2).bin",
		"2A2_18446744073709551616 (1).bin",
	}
	got := slices.Clone(want)
	slices.Reverse(got)
	sortChunkFiles(got)
	if !slices.Equal(got, want) {
		t.Fatalf("decorations changed numeric ordering: %q", got)
	}
}

func TestFormatterReadsDecoratedChunks(t *testing.T) {
	ctx := context.Background()
	for _, format := range []Format{FormatBin, FormatPNG} {
		t.Run(string(format), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "2A2")
			formatter := GetFormatter(format)
			for _, n := range []int{1, 2, 10000} {
				if err := WriteNamedChunk(ctx, formatter, dir, "2A2", n, []byte(fmt.Sprint(n))); err != nil {
					t.Fatal(err)
				}
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				ext := filepath.Ext(entry.Name())
				name := strings.ToLower(strings.TrimSuffix(entry.Name(), ext)) + " (copy_50000)" + strings.ToUpper(ext)
				if err := os.Rename(filepath.Join(dir, entry.Name()), filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
			}
			for _, n := range []int{10000, 2, 1} {
				if got, err := formatter.ReadChunk(ctx, dir, 0, n); err != nil || string(got) != fmt.Sprint(n) {
					t.Fatalf("chunk %d: got %q, %v", n, got, err)
				}
			}
		})
	}
}
