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

func TestDecodeRejectsMismatchedChunkSizesCLI(t *testing.T) {
	ctx := context.Background()
	for _, format := range []padlock.Format{padlock.FormatBin, padlock.FormatPNG} {
		for _, archive := range []bool{false, true} {
			for _, compression := range []padlock.Compression{padlock.CompressionNone, padlock.CompressionGzip} {
				for index, collection := range []string{"2A2", "2B2"} {
					t.Run(fmt.Sprintf("%s/tar=%t/gzip=%t/mismatch=%s", format, archive, compression == padlock.CompressionGzip, collection), func(t *testing.T) {
						base := t.TempDir()
						input, encoded, restored := filepath.Join(base, "input"), filepath.Join(base, "encoded"), filepath.Join(base, "restored")
						if err := os.Mkdir(input, 0700); err != nil {
							t.Fatal(err)
						}
						for name, contents := range map[string]string{"a.txt": "first file\n", "b.txt": "second file\n"} {
							if err := os.WriteFile(filepath.Join(input, name), []byte(contents), 0600); err != nil {
								t.Fatal(err)
							}
						}
						if err := padlock.EncodeDirectory(ctx, padlock.EncodeConfig{
							InputDir: input, OutputDir: encoded, N: 2, K: 2,
							Format: format, Compression: compression, ChunkSize: 65536, RNG: pad.NewCryptoRand(),
						}); err != nil {
							t.Fatal(err)
						}
						formatter := file.GetFormatter(format)
						dir := filepath.Join(encoded, collection)
						frame, err := formatter.ReadChunk(ctx, dir, index, 1)
						if err != nil {
							t.Fatal(err)
						}
						headerLength := int(frame[0])
						fields := strings.Split(string(frame[1:1+headerLength]), ":")
						originalSize, err := strconv.Atoi(fields[2])
						if err != nil {
							t.Fatal(err)
						}
						shortened := 1024
						if compression == padlock.CompressionGzip {
							shortened = originalSize / 2
						}
						if shortened < 1 || shortened >= originalSize {
							t.Fatalf("fixture cannot shorten chunk from %d to %d bytes", originalSize, shortened)
						}
						// An uncompressed 1024-byte prefix contains the complete first
						// file. This used to report success while omitting the second.
						fields[2] = strconv.Itoa(shortened)
						header := strings.Join(fields, ":")
						changed := append([]byte{byte(len(header))}, header...)
						changed = append(changed, frame[1+headerLength:1+headerLength+shortened]...)
						entries, err := os.ReadDir(dir)
						if err != nil || len(entries) != 1 {
							t.Fatalf("expected one collection chunk, got %v; err=%v", entries, err)
						}
						if err := os.Remove(filepath.Join(dir, entries[0].Name())); err != nil {
							t.Fatal(err)
						}
						if err := file.WriteNamedChunk(ctx, formatter, dir, collection, 1, changed); err != nil {
							t.Fatal(err)
						}
						if archive {
							for _, name := range []string{"2A2", "2B2"} {
								collectionDir := filepath.Join(encoded, name)
								if _, err := file.TarCollection(ctx, collectionDir); err != nil {
									t.Fatal(err)
								}
								if err := os.RemoveAll(collectionDir); err != nil {
									t.Fatal(err)
								}
							}
						}
						output, err := runCLI(t, "decode", encoded, restored)
						var exitErr *exec.ExitError
						if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
							t.Fatalf("expected normal decode failure, got %v:\n%s", err, output)
						}
						for _, detail := range []string{"chunk 1 size mismatch", "2A2", "2B2"} {
							if !strings.Contains(output, detail) {
								t.Errorf("missing %q in decode error:\n%s", detail, output)
							}
						}
						if strings.Contains(output, "Decode complete") {
							t.Errorf("reported a completed decode despite inconsistent chunks:\n%s", output)
						}
						entries, err = os.ReadDir(restored)
						if err != nil || len(entries) != 0 {
							t.Fatalf("inconsistent first chunk wrote restored files: %v; err=%v", entries, err)
						}
					})
				}
			}
		}
	}
}
