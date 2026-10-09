// Copyright 2025 Ray Ozzie. All rights reserved.

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rayozzie/padlock/internal/testarchive"
	"github.com/rayozzie/padlock/pkg/file"
	"github.com/rayozzie/padlock/pkg/pad"
	"github.com/rayozzie/padlock/pkg/padlock"
)

func assertSparseCLIFailure(t *testing.T, output string, err error) {
	t.Helper()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("expected sparse-archive failure, got %v:\n%s", err, output)
	}
	if !strings.Contains(output, file.ErrSparseTarEntry.Error()) {
		t.Errorf("missing sparse-archive diagnostic:\n%s", output)
	}
	// Check the final returned error, not merely an earlier deserializer log.
	_, finalError, found := strings.Cut(output, " decode failed:")
	if !found || !strings.Contains(finalError, file.ErrSparseTarEntry.Error()) {
		t.Errorf("final error lost sparse-archive cause:\n%s", output)
	}
	for _, success := range []string{"Decode complete", "Decoding completed successfully", "DRY RUN SIZE REPORT"} {
		if strings.Contains(output, success) {
			t.Errorf("sparse archive reported %q:\n%s", success, output)
		}
	}
}

func TestDecodeRejectsSparseCollectionCLI(t *testing.T) {
	for _, archiveName := range []string{"2A2.tar", "renamed.tar"} {
		for _, entryName := range []string{"2A2_0001.bin", "IMG2A2_0001.PNG", "ignored.txt"} {
			for _, dryRun := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/dryrun=%t", archiveName, entryName, dryRun), func(t *testing.T) {
					base := t.TempDir()
					input, output := filepath.Join(base, "input"), filepath.Join(base, "output")
					for _, dir := range []string{input, output} {
						if err := os.Mkdir(dir, 0700); err != nil {
							t.Fatal(err)
						}
					}
					// Keep the hole small even if the rejection regresses.
					if err := os.WriteFile(filepath.Join(input, archiveName), testarchive.Sparse(entryName, 1<<20, "pax1.0"), 0600); err != nil {
						t.Fatal(err)
					}
					marker := filepath.Join(output, "keep.txt")
					if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
						t.Fatal(err)
					}
					args := []string{"decode", input, output, "-clear"}
					if dryRun {
						args = append(args, "-dryrun")
					}
					log, err := runCLI(t, args...)
					assertSparseCLIFailure(t, log, err)
					if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
						t.Fatalf("sparse-archive preflight cleared the destination: %q, %v", data, err)
					}
				})
			}
		}
	}
}

func TestDecodeRejectsSparseDirectoryArchiveCLI(t *testing.T) {
	ctx := context.Background()
	for _, format := range []file.Format{file.FormatBin, file.FormatPNG} {
		for _, archive := range []bool{false, true} {
			for _, compressed := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/tar=%t/gzip=%t", format, archive, compressed), func(t *testing.T) {
					encoded := filepath.Join(t.TempDir(), "encoded")
					if err := os.Mkdir(encoded, 0700); err != nil {
						t.Fatal(err)
					}
					encoder, err := pad.NewPadForEncode(ctx, 2, 2)
					if err != nil {
						t.Fatal(err)
					}
					var source io.Reader = bytes.NewReader(testarchive.Sparse("hole.bin", 1<<20, "pax1.0"))
					if compressed {
						source = file.CompressStreamToStream(ctx, source)
						defer source.(io.Closer).Close()
					}
					err = encoder.Encode(ctx, 4096, source, pad.NewCryptoRand(),
						func(name string, chunk int, _ string) (io.WriteCloser, error) {
							if archive {
								writer, err := file.NewTarChunkWriter(ctx, filepath.Join(encoded, name+".tar"), name, format)
								if err != nil {
									return nil, err
								}
								writer.ChunkNum = chunk
								return writer, nil
							}
							return file.NewChunkWriter(ctx, file.GetFormatter(format), filepath.Join(encoded, name), 0, chunk), nil
						}, string(format))
					if archive {
						err = errors.Join(err, file.FinalizeAllTarWriters(ctx))
					}
					if err != nil {
						t.Fatal(err)
					}
					for _, dryRun := range []bool{false, true} {
						t.Run(fmt.Sprintf("dryrun=%t", dryRun), func(t *testing.T) {
							output := filepath.Join(t.TempDir(), "restored")
							args := []string{"decode", encoded, output}
							if dryRun {
								args = append(args, "-dryrun")
							}
							log, err := runCLI(t, args...)
							assertSparseCLIFailure(t, log, err)
							if _, err := os.Lstat(filepath.Join(output, "hole.bin")); !os.IsNotExist(err) {
								t.Fatalf("sparse restore created output: %v", err)
							}
							if dryRun {
								if _, err := os.Stat(output); !os.IsNotExist(err) {
									t.Fatalf("dry run created a destination: %v", err)
								}
							}
						})
					}
				})
			}
		}
	}
}

func TestDecodeRejectsSparseExtraInputCLI(t *testing.T) {
	input, outputs, _ := cliFixture(t, 1)
	ctx := context.Background()
	if err := padlock.EncodeDirectory(ctx, padlock.EncodeConfig{
		InputDir: input, OutputDir: outputs[0], N: 2, K: 2,
		Format: file.FormatBin, ChunkSize: 4096, RNG: pad.NewCryptoRand(),
		Compression: padlock.CompressionGzip, ArchiveCollections: true,
	}); err != nil {
		t.Fatal(err)
	}
	extra := t.TempDir()
	if err := os.WriteFile(filepath.Join(extra, "2A2.tar"), testarchive.Sparse("2A2_0001.bin", 1<<20, "pax1.0"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, dryRun := range []bool{false, true} {
		output := filepath.Join(t.TempDir(), "restored")
		args := []string{"decode", outputs[0], extra, output}
		if dryRun {
			args = append(args, "-dryrun")
		}
		log, err := runCLI(t, args...)
		assertSparseCLIFailure(t, log, err)
		if _, err := os.Stat(output); !os.IsNotExist(err) {
			t.Fatalf("sparse extra input created a destination: %v", err)
		}
	}
}
