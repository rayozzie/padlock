// Copyright 2025 Ray Ozzie. All rights reserved.

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rayozzie/padlock/pkg/file"
	"github.com/rayozzie/padlock/pkg/pad"
	"github.com/rayozzie/padlock/pkg/padlock"
)

func TestDecodeRejectsMatchingTruncationCLI(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name        string
		chunk, keep int
	}{
		{"partial_header", 128, 1},
		{"missing_second_file", 1024, 1},
		{"missing_both_end_blocks", 1024, 2},
		{"missing_last_end_block", 512, 5},
	} {
		for _, format := range []padlock.Format{padlock.FormatBin, padlock.FormatPNG} {
			for _, archive := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/tar=%t", tc.name, format, archive), func(t *testing.T) {
					base := t.TempDir()
					input, encoded := filepath.Join(base, "input"), filepath.Join(base, "encoded")
					if err := os.Mkdir(input, 0700); err != nil {
						t.Fatal(err)
					}
					for name, data := range map[string]string{"a.txt": "first file\n", "b.txt": "second file\n"} {
						if err := os.WriteFile(filepath.Join(input, name), []byte(data), 0600); err != nil {
							t.Fatal(err)
						}
					}
					if err := padlock.EncodeDirectory(ctx, padlock.EncodeConfig{
						InputDir: input, OutputDir: encoded, N: 2, K: 2,
						Format: format, ChunkSize: tc.chunk, RNG: pad.NewCryptoRand(), Compression: padlock.CompressionNone,
					}); err != nil {
						t.Fatal(err)
					}
					for _, name := range []string{"2A2", "2B2"} {
						dir := filepath.Join(encoded, name)
						entries, err := os.ReadDir(dir)
						if err != nil || len(entries) <= tc.keep {
							t.Fatalf("fixture needs more than %d chunks: %v; err=%v", tc.keep, entries, err)
						}
						// Leave identical, unmodified chunk prefixes in every collection.
						for _, entry := range entries[tc.keep:] {
							if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
								t.Fatal(err)
							}
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
					for _, dryRun := range []bool{false, true} {
						t.Run(fmt.Sprintf("dryrun=%t", dryRun), func(t *testing.T) {
							restored := filepath.Join(t.TempDir(), "restored")
							args := []string{"decode", encoded, restored}
							if dryRun {
								args = append(args, "-dryrun")
							}
							output, err := runCLI(t, args...)
							var exitErr *exec.ExitError
							if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
								t.Fatalf("expected incomplete-archive failure, got %v:\n%s", err, output)
							}
							if !strings.Contains(output, "unexpected EOF") || !strings.Contains(output, "tar") {
								t.Errorf("missing incomplete-archive diagnostic:\n%s", output)
							}
							for _, success := range []string{"Decode complete", "Decoding completed successfully", "DRY RUN SIZE REPORT"} {
								if strings.Contains(output, success) {
									t.Errorf("incomplete archive reported %q:\n%s", success, output)
								}
							}
							if dryRun {
								if _, err := os.Stat(restored); !os.IsNotExist(err) {
									t.Errorf("dry run created output directory: %v", err)
								}
							} else if tc.name == "missing_second_file" {
								got, err := os.ReadFile(filepath.Join(restored, "a.txt"))
								if err != nil || string(got) != "first file\n" {
									t.Fatalf("valid prefix was not recovered before failure: %q; err=%v", got, err)
								}
								if _, err := os.Stat(filepath.Join(restored, "b.txt")); !os.IsNotExist(err) {
									t.Errorf("restored a missing file: %v", err)
								}
							}
						})
					}
				})
			}
		}
	}
}

func TestDecodeCompleteArchiveDryRunCLI(t *testing.T) {
	ctx := context.Background()
	for _, compression := range []padlock.Compression{padlock.CompressionNone, padlock.CompressionGzip} {
		t.Run(fmt.Sprintf("gzip=%t", compression == padlock.CompressionGzip), func(t *testing.T) {
			input, outputs, _ := cliFixture(t, 1)
			if err := padlock.EncodeDirectory(ctx, padlock.EncodeConfig{
				InputDir: input, OutputDir: outputs[0], N: 2, K: 2,
				Format: padlock.FormatBin, ChunkSize: 128, RNG: pad.NewCryptoRand(), Compression: compression,
			}); err != nil {
				t.Fatal(err)
			}
			restored := filepath.Join(t.TempDir(), "restored")
			output, err := runCLI(t, "decode", outputs[0], restored, "-dryrun")
			if err != nil || !strings.Contains(output, "DRY RUN SIZE REPORT") {
				t.Fatalf("complete archive failed dry run: %v:\n%s", err, output)
			}
			if _, err := os.Stat(restored); !os.IsNotExist(err) {
				t.Errorf("dry run created output directory: %v", err)
			}
		})
	}
}

func TestDecodeRejectsIncompleteGzipEndingCLI(t *testing.T) {
	ctx := context.Background()
	for _, truncated := range []bool{false, true} {
		t.Run(fmt.Sprintf("truncated=%t", truncated), func(t *testing.T) {
			input, outputs, _ := cliFixture(t, 1)
			if err := padlock.EncodeDirectory(ctx, padlock.EncodeConfig{
				InputDir: input, OutputDir: outputs[0], N: 2, K: 2,
				Format: padlock.FormatBin, ChunkSize: 65536, RNG: pad.NewCryptoRand(), Compression: padlock.CompressionGzip,
			}); err != nil {
				t.Fatal(err)
			}
			for _, collection := range []string{"2A2", "2B2"} {
				path := filepath.Join(outputs[0], collection, collection+"_0001.bin")
				frame, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if truncated {
					// Both collections still agree on their shortened length, and
					// their decoded bytes include the whole TAR but no gzip footer.
					headerLength := int(frame[0])
					fields := strings.Split(string(frame[1:1+headerLength]), ":")
					size, err := strconv.Atoi(fields[2])
					if err != nil || size <= 8 {
						t.Fatalf("invalid fixture header: %v; err=%v", fields, err)
					}
					fields[2] = strconv.Itoa(size - 8)
					header := strings.Join(fields, ":")
					shortened := append([]byte{byte(len(header))}, header...)
					frame = append(shortened, frame[1+headerLength:len(frame)-8]...)
				} else if collection == "2B2" {
					// In 2-of-2 encoding this flips the same bit in the gzip CRC.
					frame[len(frame)-8] ^= 1
				}
				if err := os.WriteFile(path, frame, 0600); err != nil {
					t.Fatal(err)
				}
			}
			for _, dryRun := range []bool{false, true} {
				t.Run(fmt.Sprintf("dryrun=%t", dryRun), func(t *testing.T) {
					restored := filepath.Join(t.TempDir(), "restored")
					args := []string{"decode", outputs[0], restored}
					if dryRun {
						args = append(args, "-dryrun")
					}
					output, err := runCLI(t, args...)
					var exitErr *exec.ExitError
					if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
						t.Fatalf("expected gzip ending failure, got %v:\n%s", err, output)
					}
					message := "invalid checksum"
					if truncated {
						message = "unexpected EOF"
					}
					if !strings.Contains(output, message) || strings.Contains(output, "Decode complete") {
						t.Fatalf("missing gzip ending diagnostic or false success:\n%s", output)
					}
				})
			}
		})
	}
}
