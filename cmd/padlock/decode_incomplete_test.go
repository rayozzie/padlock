// Copyright 2025 Ray Ozzie. All rights reserved.

package main

import (
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

func TestDecodeRejectsMissingTrailingChunksCLI(t *testing.T) {
	ctx := context.Background()
	data := make([]byte, 4097)
	_, _ = rand.New(rand.NewSource(1)).Read(data)
	for _, format := range []padlock.Format{padlock.FormatBin, padlock.FormatPNG} {
		for _, archive := range []bool{false, true} {
			for _, compression := range []padlock.Compression{padlock.CompressionNone, padlock.CompressionGzip} {
				for _, incomplete := range []string{"2A2", "2B2"} {
					t.Run(fmt.Sprintf("%s/tar=%t/gzip=%t/missing=%s", format, archive, compression == padlock.CompressionGzip, incomplete), func(t *testing.T) {
						base := t.TempDir()
						input, encoded, restored := filepath.Join(base, "input"), filepath.Join(base, "encoded"), filepath.Join(base, "restored")
						if err := os.Mkdir(input, 0700); err != nil {
							t.Fatal(err)
						}
						// Without compression, the first 1024-byte chunk holds a complete
						// file. Accepting EOF there previously reported a successful restore
						// while silently omitting the second file.
						for name, contents := range map[string][]byte{"a.txt": []byte("first file"), "b.bin": data} {
							if err := os.WriteFile(filepath.Join(input, name), contents, 0600); err != nil {
								t.Fatal(err)
							}
						}
						if err := padlock.EncodeDirectory(ctx, padlock.EncodeConfig{
							InputDir: input, OutputDir: encoded, N: 2, K: 2,
							Format: format, Compression: compression, ChunkSize: 1024, RNG: pad.NewCryptoRand(),
						}); err != nil {
							t.Fatal(err)
						}
						collectionDir := filepath.Join(encoded, incomplete)
						entries, err := os.ReadDir(collectionDir)
						if err != nil {
							t.Fatal(err)
						}
						if len(entries) < 2 {
							t.Fatal("fixture needs multiple chunks to remove the tail")
						}
						for _, entry := range entries[1:] {
							if err := os.Remove(filepath.Join(collectionDir, entry.Name())); err != nil {
								t.Fatal(err)
							}
						}
						if archive {
							// Create well-formed outer archives, one with missing chunks.
							for _, name := range []string{"2A2", "2B2"} {
								dir := filepath.Join(encoded, name)
								if _, err := file.TarCollection(ctx, dir); err != nil {
									t.Fatal(err)
								}
								if err := os.RemoveAll(dir); err != nil {
									t.Fatal(err)
								}
							}
						}
						output, err := runCLI(t, "decode", encoded, restored)
						if err == nil {
							t.Fatalf("CLI reported success with missing trailing chunks:\n%s", output)
						}
						if !strings.Contains(output, incomplete) || !strings.Contains(output, "missing chunk 2") {
							t.Fatalf("CLI did not identify the incomplete collection and missing chunk:\n%s", output)
						}
						if strings.Contains(output, "Decode complete") {
							t.Fatalf("CLI reported a completed decode despite missing chunks:\n%s", output)
						}
					})
				}
			}
		}
	}
}
