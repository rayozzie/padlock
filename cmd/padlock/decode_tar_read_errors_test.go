// Copyright 2025 Ray Ozzie. All rights reserved.

package main

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rayozzie/padlock/pkg/file"
	"github.com/rayozzie/padlock/pkg/pad"
	"github.com/rayozzie/padlock/pkg/padlock"
)

// Make an ordinary TAR with a deliberately incomplete member body. A target
// of zero appends an incomplete non-chunk entry after all valid chunks.
func truncateCollectionTARBody(t *testing.T, original []byte, target int) ([]byte, string) {
	t.Helper()
	r := tar.NewReader(bytes.NewReader(original))
	var truncated bytes.Buffer
	w := tar.NewWriter(&truncated)
	for entry := 1; ; entry++ {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if entry == target {
			if h.Size < 2 {
				t.Fatal("fixture needs a nonempty chunk body")
			}
			if _, err := io.CopyN(w, r, h.Size/2); err != nil {
				t.Fatal(err)
			}
			return truncated.Bytes(), h.Name
		}
		if _, err := io.Copy(w, r); err != nil {
			t.Fatal(err)
		}
	}
	if target != 0 {
		t.Fatalf("fixture lacks chunk %d", target)
	}
	const name = "ignored.txt"
	if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: 4096}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("partial body")); err != nil {
		t.Fatal(err)
	}
	return truncated.Bytes(), name
}

func TestDecodeReportsTAREntryReadFailureCLI(t *testing.T) {
	for _, format := range []file.Format{file.FormatBin, file.FormatPNG} {
		for _, compression := range []padlock.Compression{padlock.CompressionNone, padlock.CompressionGzip} {
			t.Run(fmt.Sprintf("%s/gzip=%t", format, compression == padlock.CompressionGzip), func(t *testing.T) {
				input, _, _ := cliFixture(t, 1)
				contents := make([]byte, 8193)
				_, _ = rand.New(rand.NewSource(31)).Read(contents)
				if err := os.WriteFile(filepath.Join(input, "source.bin"), contents, 0600); err != nil {
					t.Fatal(err)
				}
				backup := filepath.Join(t.TempDir(), "backup")
				if err := padlock.EncodeDirectory(context.Background(), padlock.EncodeConfig{
					InputDir: input, OutputDir: backup, N: 2, K: 2, ChunkSize: 2048,
					Format: format, Compression: compression, ArchiveCollections: true, RNG: pad.NewCryptoRand(),
				}); err != nil {
					t.Fatal(err)
				}
				original, err := os.ReadFile(filepath.Join(backup, "2A2.tar"))
				if err != nil {
					t.Fatal(err)
				}
				for _, tc := range []struct {
					name, archive, operation string
					chunk                    int
				}{
					{"first_chunk", "2A2.tar", "read TAR entry", 1},
					{"later_chunk", "renamed.tar", "read TAR entry", 2},
					{"ignored_entry", "2A2.tar", "skip TAR entry", 0},
				} {
					broken, member := truncateCollectionTARBody(t, original, tc.chunk)
					for _, dry := range []bool{false, true} {
						t.Run(fmt.Sprintf("%s/dry=%t", tc.name, dry), func(t *testing.T) {
							encoded, output := t.TempDir(), t.TempDir()
							archive := filepath.Join(encoded, tc.archive)
							if err := os.WriteFile(archive, broken, 0600); err != nil {
								t.Fatal(err)
							}
							copyBackupCollection(t, backup, encoded, "2B2", true)
							marker := filepath.Join(output, "keep")
							if err := os.WriteFile(marker, []byte("keep me"), 0600); err != nil {
								t.Fatal(err)
							}
							args := []string{"decode", encoded, output, "-clear"}
							if dry {
								args = append(args, "-dryrun")
							}
							log, err := runCLI(t, args...)
							var exitError *exec.ExitError
							if !errors.As(err, &exitError) || exitError.ExitCode() != 1 {
								t.Fatalf("expected read failure, got %v:\n%s", err, log)
							}
							_, finalError, found := strings.Cut(log, " decode failed:")
							if !found {
								t.Fatalf("final error missing:\n%s", log)
							}
							for _, want := range []string{tc.operation, member, archive, "unexpected EOF"} {
								if !strings.Contains(finalError, want) {
									t.Errorf("final error lost %q:\n%s", want, finalError)
								}
							}
							for _, unwanted := range []string{"read TAR header", "error reading TAR header", "Decode complete", "Decoding completed successfully", "DRY RUN SIZE REPORT"} {
								if strings.Contains(log, unwanted) {
									t.Errorf("misreported body failure as %q:\n%s", unwanted, log)
								}
							}
							if dry || tc.chunk == 1 {
								if got, err := os.ReadFile(marker); err != nil || string(got) != "keep me" {
									t.Errorf("preflight failure/dry run changed destination: %q, %v", got, err)
								}
								if entries, err := os.ReadDir(output); err != nil || len(entries) != 1 {
									t.Errorf("preflight failure/dry run added output: %v, %v", entries, err)
								}
							}
						})
					}
				}
			})
		}
	}
}
