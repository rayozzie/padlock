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

func TestDuplicateCollectionsCLI(t *testing.T) {
	ctx := context.Background()
	for _, format := range []file.Format{file.FormatBin, file.FormatPNG} {
		for _, compression := range []padlock.Compression{padlock.CompressionGzip, padlock.CompressionNone} {
			t.Run(fmt.Sprintf("%s/gzip=%t", format, compression == padlock.CompressionGzip), func(t *testing.T) {
				input, _, _ := cliFixture(t, 1)
				want := make([]byte, 8193)
				_, _ = rand.New(rand.NewSource(17)).Read(want)
				if err := os.WriteFile(filepath.Join(input, "source.bin"), want, 0600); err != nil {
					t.Fatal(err)
				}
				backup := filepath.Join(t.TempDir(), "backup")
				if err := padlock.EncodeDirectory(ctx, padlock.EncodeConfig{
					InputDir: input, OutputDir: backup, N: 5, K: 3, ChunkSize: 4096,
					Format: format, Compression: compression, RNG: pad.NewCryptoRand(),
				}); err != nil {
					t.Fatal(err)
				}
				// Repack all collections without deleting their loose originals.
				for _, name := range []string{"3A5", "3B5", "3C5", "3D5", "3E5"} {
					if _, err := file.TarCollection(ctx, filepath.Join(backup, name)); err != nil {
						t.Fatal(err)
					}
				}
				duplicateRoot := t.TempDir()
				copyBackupCollection(t, backup, duplicateRoot, "3A5", false)
				cases := []struct {
					name   string
					inputs []string
				}{
					{"loose_and_tar", []string{backup}},
					{"repeated_parent", []string{backup, backup}},
					{"over_26_inputs", []string{backup, backup, backup}},
					{"direct_and_copied", []string{filepath.Join(backup, "3E5"), filepath.Join(duplicateRoot, "3A5"), filepath.Join(backup, "3C5"), filepath.Join(backup, "3A5")}},
				}
				for _, tc := range cases {
					for _, dryRun := range []bool{false, true} {
						t.Run(fmt.Sprintf("%s/dry=%t", tc.name, dryRun), func(t *testing.T) {
							output := filepath.Join(t.TempDir(), "restore")
							args := append([]string{"decode"}, tc.inputs...)
							args = append(args, output)
							if dryRun {
								args = append(args, "-dryrun")
							}
							out, err := runCLI(t, args...)
							if err != nil {
								t.Fatalf("duplicate restore failed: %v\n%s", err, out)
							}
							if dryRun {
								if _, err := os.Stat(output); !os.IsNotExist(err) {
									t.Fatalf("dry run created output: %v", err)
								}
							} else if got, err := os.ReadFile(filepath.Join(output, "source.bin")); err != nil || !bytes.Equal(got, want) {
								t.Fatalf("restored data differs: %v", err)
							}
						})
					}
				}
				// Change a later payload in the loose E copy. The TAR replica and
				// enough other intact collections remain; E is outside selected ABC.
				directory := filepath.Join(backup, "3E5")
				formatter := file.GetFormatter(format)
				frame, err := formatter.ReadChunk(ctx, directory, 4, 2)
				if err != nil {
					t.Fatal(err)
				}
				frame[len(frame)-1] ^= 1
				entries, err := os.ReadDir(directory)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if strings.Contains(entry.Name(), "_0002.") {
						if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil {
							t.Fatal(err)
						}
					}
				}
				if err := file.WriteNamedChunk(ctx, formatter, directory, "3E5", 2, frame); err != nil {
					t.Fatal(err)
				}
				for _, dryRun := range []bool{false, true} {
					args := []string{"decode", backup, filepath.Join(t.TempDir(), "conflict")}
					if dryRun {
						args = append(args, "-dryrun")
					}
					out, err := runCLI(t, args...)
					if err == nil || !strings.Contains(out, "conflicting duplicate collection 3E5 at chunk 2") || strings.Contains(out, "Decode complete") {
						t.Fatalf("conflicting duplicate was accepted or misreported: %v\n%s", err, out)
					}
				}
			})
		}
	}
}

func TestDuplicateThresholdPreservesDestinationCLI(t *testing.T) {
	input, _, _ := cliFixture(t, 1)
	for _, format := range []file.Format{file.FormatBin, file.FormatPNG} {
		t.Run(string(format), func(t *testing.T) {
			backup := encodeBackupDirectory(t, input, format, false)
			for _, name := range []string{"2B3", "2C3"} {
				if err := os.RemoveAll(filepath.Join(backup, name)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := file.TarCollection(context.Background(), filepath.Join(backup, "2A3")); err != nil {
				t.Fatal(err)
			}
			for _, dryRun := range []bool{false, true} {
				for _, existing := range []bool{false, true} {
					t.Run(fmt.Sprintf("dry=%t/existing=%t", dryRun, existing), func(t *testing.T) {
						output := filepath.Join(t.TempDir(), "restore")
						if existing {
							if err := os.Mkdir(output, 0700); err != nil {
								t.Fatal(err)
							}
							if err := os.WriteFile(filepath.Join(output, "keep"), []byte("keep me"), 0600); err != nil {
								t.Fatal(err)
							}
						}
						args := []string{"decode", backup, output, "-clear"}
						if dryRun {
							args = append(args, "-dryrun")
						}
						out, err := runCLI(t, args...)
						if err == nil || !strings.Contains(out, "not enough distinct collections") || !strings.Contains(out, "1 < 2") {
							t.Fatalf("threshold failure missing: %v\n%s", err, out)
						}
						if existing {
							got, err := os.ReadFile(filepath.Join(output, "keep"))
							entries, readErr := os.ReadDir(output)
							if err != nil || string(got) != "keep me" || readErr != nil || len(entries) != 1 {
								t.Fatalf("threshold failure changed destination: %q, %v, %v", got, err, readErr)
							}
						} else if _, err := os.Stat(output); !os.IsNotExist(err) {
							t.Fatalf("threshold failure created destination: %v", err)
						}
					})
				}
			}
		})
	}
}

func TestDuplicateFirstPayloadPreservesDestinationCLI(t *testing.T) {
	ctx := context.Background()
	for _, format := range []file.Format{file.FormatBin, file.FormatPNG} {
		t.Run(string(format), func(t *testing.T) {
			input, _, _ := cliFixture(t, 1)
			backup := filepath.Join(t.TempDir(), "backup")
			if err := padlock.EncodeDirectory(ctx, padlock.EncodeConfig{
				InputDir: input, OutputDir: backup, N: 5, K: 3, ChunkSize: 4096,
				Format: format, Compression: padlock.CompressionGzip, RNG: pad.NewCryptoRand(),
			}); err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(backup, "3E5")
			if _, err := file.TarCollection(ctx, directory); err != nil {
				t.Fatal(err)
			}
			formatter := file.GetFormatter(format)
			frame, err := formatter.ReadChunk(ctx, directory, 4, 1)
			if err != nil {
				t.Fatal(err)
			}
			frame[len(frame)-1] ^= 1
			entries, err := os.ReadDir(directory)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.Contains(entry.Name(), "_0001.") {
					if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := file.WriteNamedChunk(ctx, formatter, directory, "3E5", 1, frame); err != nil {
				t.Fatal(err)
			}
			for _, dry := range []bool{false, true} {
				for _, existing := range []bool{false, true} {
					t.Run(fmt.Sprintf("dry=%t/existing=%t", dry, existing), func(t *testing.T) {
						output := filepath.Join(t.TempDir(), "restore")
						if existing {
							if err := os.Mkdir(output, 0700); err != nil {
								t.Fatal(err)
							}
							if err := os.WriteFile(filepath.Join(output, "keep"), []byte("keep me"), 0600); err != nil {
								t.Fatal(err)
							}
						}
						args := []string{"decode", backup, output, "-clear"}
						if dry {
							args = append(args, "-dryrun")
						}
						log, err := runCLI(t, args...)
						if err == nil || !strings.Contains(log, "conflicting duplicate collection 3E5 at chunk 1") || strings.Contains(log, "Decode complete") {
							t.Fatalf("first conflict accepted or misreported: %v\n%s", err, log)
						}
						if existing {
							entries, err := os.ReadDir(output)
							if err != nil || len(entries) != 1 || entries[0].Name() != "keep" {
								t.Fatalf("conflict changed destination: %v, %v", entries, err)
							}
							if got, err := os.ReadFile(filepath.Join(output, "keep")); err != nil || string(got) != "keep me" {
								t.Fatalf("conflict cleared destination: %q, %v", got, err)
							}
						} else if _, err := os.Lstat(output); !os.IsNotExist(err) {
							t.Fatalf("conflict created destination: %v", err)
						}
					})
				}
			}
		})
	}
}
