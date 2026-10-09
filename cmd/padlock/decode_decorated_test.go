// Copyright 2025 Ray Ozzie. All rights reserved.

package main

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rayozzie/padlock/pkg/file"
	"github.com/rayozzie/padlock/pkg/pad"
	"github.com/rayozzie/padlock/pkg/padlock"
)

func TestDecodeDecoratedChunkNamesCLI(t *testing.T) {
	for _, format := range []file.Format{file.FormatBin, file.FormatPNG} {
		input, _, _ := cliFixture(t, 1)
		want := make([]byte, 14003)
		_, _ = rand.New(rand.NewSource(41)).Read(want)
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
		for _, spelling := range []string{"copy_suffix", "lowercase", "conflicted_suffix"} {
			for _, layout := range []string{"loose", "named-tar", "renamed-tar"} {
				t.Run(fmt.Sprintf("%s/%s/%s", format, spelling, layout), func(t *testing.T) {
					encoded := t.TempDir()
					for _, name := range []string{"2A2", "2B2"} {
						copyBackupCollection(t, backup, encoded, name, false)
						dir := filepath.Join(encoded, name)
						entries, err := os.ReadDir(dir)
						if err != nil {
							t.Fatal(err)
						}
						for _, entry := range entries {
							ext := filepath.Ext(entry.Name())
							stem := strings.TrimSuffix(entry.Name(), ext)
							switch spelling {
							case "copy_suffix":
								stem += " (1)"
							case "lowercase":
								stem, ext = strings.ToLower(stem), strings.ToLower(ext)
							case "conflicted_suffix":
								stem += " (conflicted copy_2026-10-09)"
							}
							if err := os.Rename(filepath.Join(dir, entry.Name()), filepath.Join(dir, stem+ext)); err != nil {
								t.Fatal(err)
							}
						}
						if layout != "loose" {
							archive := name + ".tar"
							if layout == "renamed-tar" {
								archive = "copy-" + archive
							}
							// Reversed storage order and sidecars must also work with
							// decorations; the suffix is not another chunk number.
							repackCollectionForTest(t, dir, filepath.Join(encoded, archive), "reverse", true)
							if err := os.RemoveAll(dir); err != nil {
								t.Fatal(err)
							}
						}
					}
					output := filepath.Join(t.TempDir(), "restore")
					if log, err := runCLI(t, "decode", encoded, output); err != nil {
						t.Fatalf("decorated collection restore failed: %v\n%s", err, log)
					}
					if got, err := os.ReadFile(filepath.Join(output, "source.bin")); err != nil || !bytes.Equal(got, want) {
						t.Fatalf("incorrect restored bytes: %v", err)
					}
				})
			}
		}
	}
}
