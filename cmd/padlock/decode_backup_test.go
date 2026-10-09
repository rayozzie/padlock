// Copyright 2025 Ray Ozzie. All rights reserved.

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rayozzie/padlock/pkg/file"
	"github.com/rayozzie/padlock/pkg/pad"
	"github.com/rayozzie/padlock/pkg/padlock"
)

func encodeBackupDirectory(t *testing.T, input string, format file.Format, archive bool) string {
	t.Helper()
	output := filepath.Join(t.TempDir(), "backup")
	if err := padlock.EncodeDirectory(context.Background(), padlock.EncodeConfig{
		InputDir: input, OutputDir: output, N: 3, K: 2, ChunkSize: 65536,
		Format: format, ArchiveCollections: archive, Compression: padlock.CompressionGzip, RNG: pad.NewCryptoRand(),
	}); err != nil {
		t.Fatal(err)
	}
	return output
}

func copyBackupCollection(t *testing.T, source, destination, name string, archive bool) {
	t.Helper()
	if archive {
		data, err := os.ReadFile(filepath.Join(source, name+".tar"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(destination, name+".tar"), data, 0600); err != nil {
			t.Fatal(err)
		}
	} else if err := os.CopyFS(filepath.Join(destination, name), os.DirFS(filepath.Join(source, name))); err != nil {
		t.Fatal(err)
	}
}

func TestMixedBackupsPreserveDestinationCLI(t *testing.T) {
	for _, format := range []file.Format{file.FormatBin, file.FormatPNG} {
		for _, archive := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/tar=%t", format, archive), func(t *testing.T) {
				input, _, _ := cliFixture(t, 1)
				// Back up the same input twice so names, thresholds, chunk numbers,
				// and payload sizes all match. Only the backup ID distinguishes them.
				first := encodeBackupDirectory(t, input, format, archive)
				second := encodeBackupDirectory(t, input, format, archive)
				for _, extra := range []bool{false, true} {
					t.Run(fmt.Sprintf("extra_collection=%t", extra), func(t *testing.T) {
						mixed := t.TempDir()
						names := []string{"2A3", "2B3"}
						if extra {
							names = append(names, "2C3")
						}
						inputs := []string{mixed}
						if extra {
							inputs = nil // Also exercise discovery from multiple inputs.
						}
						for i, name := range names {
							source, destination := first, mixed
							if i == len(names)-1 {
								source = second
							}
							if extra {
								destination = t.TempDir()
								inputs = append(inputs, destination)
							}
							copyBackupCollection(t, source, destination, name, archive)
						}
						for _, dryRun := range []bool{false, true} {
							restored := t.TempDir()
							keep := filepath.Join(restored, "existing-backup.txt")
							if err := os.WriteFile(keep, []byte("preserve this"), 0600); err != nil {
								t.Fatal(err)
							}
							args := append([]string{"decode"}, inputs...)
							args = append(args, restored, "-clear")
							if dryRun {
								args = append(args, "-dryrun")
							}
							out, err := runCLI(t, args...)
							if err == nil || !strings.Contains(out, "different backups") || strings.Contains(out, "panic:") || strings.Contains(out, "Decode complete") {
								t.Fatalf("mixed backups were not rejected clearly: %v\n%s", err, out)
							}
							got, err := os.ReadFile(keep)
							entries, readErr := os.ReadDir(restored)
							if err != nil || string(got) != "preserve this" || readErr != nil || len(entries) != 1 {
								t.Fatalf("mixed backup changed the destination: %v, %v; %v", err, readErr, entries)
							}
						}
						missing := filepath.Join(t.TempDir(), "not-created")
						args := append([]string{"decode"}, inputs...)
						args = append(args, missing)
						if out, err := runCLI(t, args...); err == nil || !strings.Contains(out, "different backups") {
							t.Fatalf("mixed backup accepted with a new destination: %v\n%s", err, out)
						}
						if _, err := os.Stat(missing); !os.IsNotExist(err) {
							t.Fatalf("mixed backup created the destination: %v", err)
						}
					})
				}
			})
		}
	}
}

func TestLegacyBackupsRestoreQuietlyCLI(t *testing.T) {
	ctx := context.Background()
	for _, format := range []file.Format{file.FormatBin, file.FormatPNG} {
		for _, archive := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/tar=%t", format, archive), func(t *testing.T) {
				input, _, want := cliFixture(t, 1)
				encoded := encodeBackupDirectory(t, input, format, false)
				for collection, name := range []string{"2A3", "2B3", "2C3"} {
					dir := filepath.Join(encoded, name)
					formatter := file.GetFormatter(format)
					entries, err := os.ReadDir(dir)
					if err != nil || len(entries) != 1 {
						t.Fatalf("expected a single-chunk fixture: %v, %v", entries, err)
					}
					frame, err := formatter.ReadChunk(ctx, dir, collection, 1)
					if err != nil {
						t.Fatal(err)
					}
					length := int(frame[0])
					fields := strings.Split(string(frame[1:1+length]), ":")
					if len(fields) != 4 {
						t.Fatal("new backup does not include an identifier")
					}
					header := strings.Join(fields[:3], ":")
					legacy := append([]byte{byte(len(header))}, header...)
					legacy = append(legacy, frame[1+length:]...)
					if err := os.Remove(filepath.Join(dir, entries[0].Name())); err != nil {
						t.Fatal(err)
					}
					if err := file.WriteNamedChunk(ctx, formatter, dir, name, 1, legacy); err != nil {
						t.Fatal(err)
					}
					if archive {
						if _, err := file.TarCollection(ctx, dir); err != nil {
							t.Fatal(err)
						}
						if err := os.RemoveAll(dir); err != nil {
							t.Fatal(err)
						}
					}
				}
				restored := filepath.Join(t.TempDir(), "restored")
				out, err := runCLI(t, "decode", encoded, restored)
				if err != nil || strings.Contains(strings.ToLower(out), "warning") {
					t.Fatalf("legacy restore failed or emitted a warning: %v\n%s", err, out)
				}
				got, err := os.ReadFile(filepath.Join(restored, "source.bin"))
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("legacy restore changed the data: %v", err)
				}
			})
		}
	}
}
