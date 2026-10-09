// Copyright 2025 Ray Ozzie. All rights reserved.

package padlock

import (
	"bytes"
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/rayozzie/padlock/pkg/file"
	"github.com/rayozzie/padlock/pkg/pad"
)

func TestDirectoryRoundTripMoreThan10000Chunks(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	input, encoded := filepath.Join(base, "input"), filepath.Join(base, "encoded")
	if err := os.Mkdir(input, 0700); err != nil {
		t.Fatal(err)
	}
	want := make([]byte, 10001)
	_, _ = rand.New(rand.NewSource(1)).Read(want)
	if err := os.WriteFile(filepath.Join(input, "source.bin"), want, 0600); err != nil {
		t.Fatal(err)
	}
	// Produce real frames with the streaming encoder, then extract the chunks.
	// This exercises loose-file restoration without thousands of individual
	// fsync calls just to construct a fixture. Boundary filenames from the loose
	// writer are covered by the file package's BIN/PNG tests.
	if err := EncodeDirectory(ctx, EncodeConfig{
		InputDir: input, OutputDir: encoded, N: 2, K: 2,
		Format: FormatBin, ArchiveCollections: true, Compression: CompressionNone,
		ChunkSize: 1, RNG: pad.NewCryptoRand(),
	}); err != nil {
		t.Fatal(err)
	}
	streamed := t.TempDir()
	for _, collection := range []string{"2A2", "2B2"} {
		archive := filepath.Join(encoded, collection+".tar")
		if _, err := file.ExtractTarCollection(ctx, archive, encoded); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(archive, filepath.Join(streamed, collection+".tar")); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(encoded, collection)
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) < 10001 {
			t.Fatalf("%s: need at least 10,001 chunks, got %d, err=%v", collection, len(entries), err)
		}
		for _, suffix := range []string{"0001", "1000", "1001", "9999", "10000", "10001"} {
			if _, err := os.Stat(filepath.Join(dir, collection+"_"+suffix+".bin")); err != nil {
				t.Fatalf("existing filename convention changed: %v", err)
			}
		}
	}

	for _, storage := range []string{"streamed-tar", "loose", "repacked-tar", "extracted-tar"} {
		t.Run(storage, func(t *testing.T) {
			backup := encoded
			if storage == "streamed-tar" {
				backup = streamed
			} else if storage != "loose" {
				backup = t.TempDir()
				for _, collection := range []string{"2A2", "2B2"} {
					archive, err := file.TarCollection(ctx, filepath.Join(encoded, collection))
					if err != nil {
						t.Fatal(err)
					}
					moved := filepath.Join(t.TempDir(), filepath.Base(archive))
					if storage == "repacked-tar" {
						moved = filepath.Join(backup, filepath.Base(archive))
					}
					if err := os.Rename(archive, moved); err != nil {
						t.Fatal(err)
					}
					if storage == "extracted-tar" {
						if _, err := file.ExtractTarCollection(ctx, moved, backup); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			for _, dryRun := range []bool{true, false} {
				restored := filepath.Join(t.TempDir(), "restored")
				if err := DecodeDirectory(ctx, DecodeConfig{
					InputDir: backup, OutputDir: restored, Compression: CompressionNone, SizeOnly: dryRun,
				}); err != nil {
					t.Fatalf("dryrun=%t: %v", dryRun, err)
				}
				if dryRun {
					if _, err := os.Stat(restored); !os.IsNotExist(err) {
						t.Fatalf("dry run created output: %v", err)
					}
					continue
				}
				got, err := os.ReadFile(filepath.Join(restored, "source.bin"))
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("restored %d bytes, want %d identical bytes, err=%v", len(got), len(want), err)
				}
			}
		})
	}
}
