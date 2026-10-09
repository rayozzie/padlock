// Copyright 2025 Ray Ozzie. All rights reserved.

package main

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/rayozzie/padlock/pkg/file"
	"github.com/rayozzie/padlock/pkg/pad"
	"github.com/rayozzie/padlock/pkg/padlock"
)

// Write archives independently of Padlock's archive helpers, as a user packing
// loose collections would. AppleDouble entries deliberately precede each chunk.
func repackCollectionForTest(t *testing.T, directory, archivePath, order string, sidecars bool) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 13 {
		t.Fatalf("fixture needs at least 13 chunks, got %d", len(entries))
	}
	switch order {
	case "reverse":
		for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
			entries[i], entries[j] = entries[j], entries[i]
		}
	case "shuffled":
		rand.New(rand.NewSource(17)).Shuffle(len(entries), func(i, j int) { entries[i], entries[j] = entries[j], entries[i] })
	}
	var archive bytes.Buffer
	w := tar.NewWriter(&archive)
	for _, entry := range entries {
		body, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(directory) + "/" + entry.Name()
		if sidecars {
			sidecar := filepath.Base(directory) + "/._" + entry.Name()
			if err := w.WriteHeader(&tar.Header{Name: sidecar, Mode: 0600, Size: 8}); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte{0, 5, 22, 7, 0, 2, 0, 0}); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeRepackedCollectionsCLI(t *testing.T) {
	for _, format := range []file.Format{file.FormatBin, file.FormatPNG} {
		t.Run(string(format), func(t *testing.T) {
			input, _, _ := cliFixture(t, 1)
			want := make([]byte, 14003)
			_, _ = rand.New(rand.NewSource(19)).Read(want)
			if err := os.WriteFile(filepath.Join(input, "source.bin"), want, 0600); err != nil {
				t.Fatal(err)
			}
			backup := filepath.Join(t.TempDir(), "backup")
			if err := padlock.EncodeDirectory(context.Background(), padlock.EncodeConfig{
				InputDir: input, OutputDir: backup, N: 2, K: 2, ChunkSize: 1024,
				Format: format, Compression: padlock.CompressionGzip, RNG: pad.NewCryptoRand(),
			}); err != nil {
				t.Fatal(err)
			}
			for _, layout := range []string{"loose", "named-tar", "renamed-tar"} {
				for _, order := range []string{"sorted", "reverse", "shuffled"} {
					for _, sidecars := range []bool{false, true} {
						if layout == "loose" && (order != "sorted" || !sidecars) {
							continue
						}
						t.Run(fmt.Sprintf("%s/%s/sidecars=%t", layout, order, sidecars), func(t *testing.T) {
							encoded := t.TempDir()
							for _, name := range []string{"2A2", "2B2"} {
								if layout == "loose" {
									copyBackupCollection(t, backup, encoded, name, false)
									dir := filepath.Join(encoded, name)
									entries, err := os.ReadDir(dir)
									if err != nil {
										t.Fatal(err)
									}
									for _, entry := range entries {
										if err := os.WriteFile(filepath.Join(dir, "._"+entry.Name()), []byte{0, 5, 22, 7, 0, 2, 0, 0}, 0600); err != nil {
											t.Fatal(err)
										}
									}
								} else {
									archiveName := name + ".tar"
									if layout == "renamed-tar" {
										archiveName = "renamed-" + archiveName
									}
									repackCollectionForTest(t, filepath.Join(backup, name), filepath.Join(encoded, archiveName), order, sidecars)
								}
							}
							output := filepath.Join(t.TempDir(), "restore")
							if out, err := runCLI(t, "decode", encoded, output); err != nil {
								t.Fatalf("restore failed: %v\n%s", err, out)
							}
							if got, err := os.ReadFile(filepath.Join(output, "source.bin")); err != nil || !bytes.Equal(got, want) {
								t.Fatalf("restored bytes differ: %v", err)
							}
						})
					}
				}
			}
		})
	}
}
