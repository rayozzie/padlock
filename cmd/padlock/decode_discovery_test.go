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
	"strings"
	"testing"

	"github.com/rayozzie/padlock/pkg/file"
	"github.com/rayozzie/padlock/pkg/pad"
	"github.com/rayozzie/padlock/pkg/padlock"
)

func TestRenamedTARDiscoveryWithoutTemporaryStorageCLI(t *testing.T) {
	for _, format := range []file.Format{file.FormatBin, file.FormatPNG} {
		for _, compression := range []padlock.Compression{padlock.CompressionNone, padlock.CompressionGzip} {
			t.Run(fmt.Sprintf("%s/gzip=%t", format, compression == padlock.CompressionGzip), func(t *testing.T) {
				input, _, _ := cliFixture(t, 1)
				want := make([]byte, 16<<10)
				_, _ = rand.New(rand.NewSource(29)).Read(want)
				if err := os.WriteFile(filepath.Join(input, "source.bin"), want, 0600); err != nil {
					t.Fatal(err)
				}
				backup := filepath.Join(t.TempDir(), "backup")
				if err := padlock.EncodeDirectory(context.Background(), padlock.EncodeConfig{
					InputDir: input, OutputDir: backup, N: 2, K: 2, ChunkSize: 4096,
					Format: format, Compression: compression, ArchiveCollections: true, RNG: pad.NewCryptoRand(),
				}); err != nil {
					t.Fatal(err)
				}
				separate := []string{t.TempDir(), t.TempDir()}
				for i, name := range []string{"2A2", "2B2"} {
					original := filepath.Join(backup, name+".tar")
					data, err := os.ReadFile(original)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(separate[i], "renamed.tar"), data, 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(original, filepath.Join(backup, name+"-renamed.tar")); err != nil {
						t.Fatal(err)
					}
				}
				var unrelated bytes.Buffer
				w := tar.NewWriter(&unrelated)
				if err := w.WriteHeader(&tar.Header{Name: "notes.txt", Mode: 0600, Size: 5}); err != nil {
					t.Fatal(err)
				}
				if _, err := w.Write([]byte("notes")); err != nil {
					t.Fatal(err)
				}
				if err := w.Close(); err != nil {
					t.Fatal(err)
				}
				for _, dir := range append([]string{backup}, separate...) {
					if err := os.WriteFile(filepath.Join(dir, "unrelated.tar"), unrelated.Bytes(), 0600); err != nil {
						t.Fatal(err)
					}
				}
				for _, multiple := range []bool{false, true} {
					for _, dry := range []bool{false, true} {
						t.Run(fmt.Sprintf("multiple=%t/dry=%t", multiple, dry), func(t *testing.T) {
							output, scratch := t.TempDir(), t.TempDir()
							marker := filepath.Join(output, "keep")
							if err := os.WriteFile(marker, []byte("keep me"), 0600); err != nil {
								t.Fatal(err)
							}
							blocked := filepath.Join(scratch, "not-a-directory")
							if err := os.WriteFile(blocked, []byte("no temporary storage"), 0600); err != nil {
								t.Fatal(err)
							}
							for _, variable := range []string{"TMPDIR", "TMP", "TEMP"} {
								t.Setenv(variable, blocked)
							}
							inputs := []string{backup}
							if multiple {
								inputs = separate
							}
							args := append([]string{"decode"}, inputs...)
							args = append(args, output, "-clear", "-verbose")
							if dry {
								args = append(args, "-dryrun")
							}
							log, err := runCLI(t, args...)
							if err != nil || strings.Contains(log, "ERROR") || strings.Contains(log, "temporary directory") || strings.Contains(log, "Extracting tar collection") {
								t.Fatalf("restore used temporary extraction or failed: %v\n%s", err, log)
							}
							entries, err := os.ReadDir(output)
							if err != nil || len(entries) != 1 {
								t.Fatalf("unexpected destination contents: %v, err=%v", entries, err)
							}
							path, expected := filepath.Join(output, "source.bin"), want
							if dry {
								path, expected = marker, []byte("keep me")
							}
							if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, expected) {
								t.Fatalf("incorrect destination contents: %v", err)
							}
							if got, err := os.ReadFile(blocked); err != nil || string(got) != "no temporary storage" {
								t.Fatalf("temporary location changed: %v", err)
							}
						})
					}
				}
			})
		}
	}
}
