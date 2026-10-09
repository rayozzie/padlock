// Copyright 2025 Ray Ozzie. All rights reserved.

package main

import (
	"archive/tar"
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

	"github.com/rayozzie/padlock/pkg/file"
	"github.com/rayozzie/padlock/pkg/pad"
)

func TestDecodeRejectsSpecialDirectoryEntriesCLI(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []byte{tar.TypeFifo, tar.TypeChar, tar.TypeBlock} {
		var inner bytes.Buffer
		writer := tar.NewWriter(&inner)
		if err := writer.WriteHeader(&tar.Header{Name: "special", Mode: 0600, Typeflag: kind}); err != nil {
			t.Fatal(err)
		}
		if err := writer.WriteHeader(&tar.Header{Name: "after.txt", Mode: 0600, Typeflag: tar.TypeReg, Size: 5}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte("after")); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		for _, format := range []file.Format{file.FormatBin, file.FormatPNG} {
			for _, archive := range []bool{false, true} {
				t.Run(fmt.Sprintf("%c/%s/tar=%t", kind, format, archive), func(t *testing.T) {
					encoded := t.TempDir()
					encoder, err := pad.NewPadForEncode(ctx, 2, 2)
					if err != nil {
						t.Fatal(err)
					}
					source := file.CompressStreamToStream(ctx, bytes.NewReader(inner.Bytes()))
					defer source.(io.Closer).Close()
					err = encoder.Encode(ctx, 4096, source, pad.NewCryptoRand(),
						func(name string, chunk int, _ string) (io.WriteCloser, error) {
							if archive {
								w, err := file.NewTarChunkWriter(ctx, filepath.Join(encoded, name+".tar"), name, format)
								if err != nil {
									return nil, err
								}
								w.ChunkNum = chunk
								return w, nil
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
						output := filepath.Join(t.TempDir(), "restored")
						args := []string{"decode", encoded, output}
						if dryRun {
							args = append(args, "-dryrun")
						}
						log, err := runCLI(t, args...)
						var exitErr *exec.ExitError
						if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || !strings.Contains(log, "unsupported tar entry type") {
							t.Fatalf("dryrun=%t: missing unsupported-entry failure: %v\n%s", dryRun, err, log)
						}
						_, finalError, found := strings.Cut(log, " decode failed:")
						if !found || !strings.Contains(finalError, "unsupported tar entry type") {
							t.Errorf("dryrun=%t: final error lost unsupported-entry cause:\n%s", dryRun, log)
						}
						for _, success := range []string{"Decode complete", "Decoding completed successfully", "DRY RUN SIZE REPORT"} {
							if strings.Contains(log, success) {
								t.Errorf("reported success despite unsupported entry: %s", log)
							}
						}
						for _, name := range []string{"special", "after.txt"} {
							if _, err := os.Lstat(filepath.Join(output, name)); !os.IsNotExist(err) {
								t.Errorf("created %q after unsupported entry: %v", name, err)
							}
						}
						if dryRun {
							if _, err := os.Stat(output); !os.IsNotExist(err) {
								t.Errorf("dry run created output: %v", err)
							}
						}
					}
				})
			}
		}
	}
}
