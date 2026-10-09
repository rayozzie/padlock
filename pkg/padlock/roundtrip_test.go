// Copyright 2025 Ray Ozzie. All rights reserved.

package padlock

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/rayozzie/padlock/pkg/file"
	"github.com/rayozzie/padlock/pkg/pad"
)

func TestDirectoryRoundTripFormats(t *testing.T) {
	ctx := context.Background()
	data := make([]byte, 8193)
	_, _ = rand.New(rand.NewSource(1)).Read(data)
	for _, format := range []Format{FormatBin, FormatPNG} {
		for _, archive := range []bool{false, true} {
			for _, compression := range []Compression{CompressionGzip, CompressionNone} {
				t.Run(fmt.Sprintf("%s/tar=%t/gzip=%t", format, archive, compression == CompressionGzip), func(t *testing.T) {
					base := t.TempDir()
					input, encoded, restored := filepath.Join(base, "input"), filepath.Join(base, "encoded"), filepath.Join(base, "restored")
					files := map[string][]byte{
						"nested/data.bin": data,
						"hello.txt":       []byte("Hello, world!\n"),
						"empty.dat":       {},
					}
					for name, contents := range files {
						path := filepath.Join(input, name)
						if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(path, contents, 0600); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.Mkdir(filepath.Join(input, "empty-directory"), 0700); err != nil {
						t.Fatal(err)
					}
					cfg := EncodeConfig{
						InputDir: input, OutputDir: encoded, N: 3, K: 2,
						Format: format, ArchiveCollections: archive, Compression: compression,
						ChunkSize: 4096, RNG: pad.NewCryptoRand(),
					}
					if err := EncodeDirectory(ctx, cfg); err != nil {
						t.Fatal(err)
					}
					// Restore using only collections A and C from this 2-of-3 backup.
					unused := filepath.Join(encoded, "2B3")
					if archive {
						unused += ".tar"
					}
					if err := os.RemoveAll(unused); err != nil {
						t.Fatal(err)
					}
					if err := DecodeDirectory(ctx, DecodeConfig{InputDir: encoded, OutputDir: restored, Compression: compression}); err != nil {
						t.Fatal(err)
					}
					for name, want := range files {
						got, err := os.ReadFile(filepath.Join(restored, name))
						if err != nil || !bytes.Equal(got, want) {
							t.Errorf("restored %s: got %d bytes, want %d identical bytes; err=%v", name, len(got), len(want), err)
						}
					}
					info, err := os.Stat(filepath.Join(restored, "empty-directory"))
					if err != nil || !info.IsDir() {
						t.Errorf("empty directory was not restored: %v", err)
					}
					count := 0
					if err := filepath.WalkDir(restored, func(_ string, entry fs.DirEntry, err error) error {
						if err == nil && !entry.IsDir() {
							count++
						}
						return err
					}); err != nil {
						t.Fatal(err)
					}
					if count != len(files) {
						t.Errorf("restored %d files, want %d", count, len(files))
					}
				})
			}
		}
	}
}

func TestEmptyDirectoryArchiveStillDecodes(t *testing.T) {
	ctx := context.Background()
	for _, archive := range []bool{false, true} {
		for _, compression := range []Compression{CompressionGzip, CompressionNone} {
			t.Run(fmt.Sprintf("tar=%t/gzip=%t", archive, compression == CompressionGzip), func(t *testing.T) {
				base := t.TempDir()
				input, encoded := filepath.Join(base, "input"), filepath.Join(base, "encoded")
				if err := os.Mkdir(input, 0700); err != nil {
					t.Fatal(err)
				}
				if err := EncodeDirectory(ctx, EncodeConfig{
					InputDir: input, OutputDir: encoded, N: 2, K: 2,
					Format: FormatBin, ArchiveCollections: archive, Compression: compression,
					ChunkSize: 4096, RNG: pad.NewCryptoRand(),
				}); err != nil {
					t.Fatal(err)
				}
				collections, _, err := file.FindCollections(ctx, encoded)
				if err != nil {
					t.Fatal(err)
				}
				readers := make([]io.Reader, len(collections))
				for i, collection := range collections {
					readers[i] = file.NewChunkReaderAdapter(ctx, file.NewCollectionReader(collection))
				}
				decoder, err := pad.NewPadForDecode(ctx, len(readers))
				if err != nil {
					t.Fatal(err)
				}
				var decoded bytes.Buffer
				if err := decoder.Decode(ctx, readers, &decoded); err != nil {
					t.Fatalf("valid backup of an empty directory failed to decode: %v", err)
				}
				var stream io.Reader = &decoded
				if compression == CompressionGzip {
					stream, err = file.DecompressStreamToStream(ctx, stream)
					if err != nil {
						t.Fatal(err)
					}
				}
				// Even an empty source produces two 512-byte TAR end records.
				// These must survive decoding rather than trigger the no-chunk guard.
				got, err := io.ReadAll(stream)
				if err != nil || !bytes.Equal(got, make([]byte, 1024)) {
					t.Fatalf("empty directory archive did not survive decoding: got %d bytes, err=%v", len(got), err)
				}
			})
		}
	}
}

func TestRestoreRejectsCollectionsWithoutChunkData(t *testing.T) {
	ctx := context.Background()
	for _, archive := range []bool{false, true} {
		t.Run(fmt.Sprintf("tar=%t", archive), func(t *testing.T) {
			base := t.TempDir()
			encoded, restored := filepath.Join(base, "encoded"), filepath.Join(base, "restored")
			for _, name := range []string{"2A2", "2B2"} {
				dir := filepath.Join(encoded, name)
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, name+"_0001.bin"), nil, 0600); err != nil {
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
			for _, compression := range []Compression{CompressionGzip, CompressionNone} {
				if err := DecodeDirectory(ctx, DecodeConfig{InputDir: encoded, OutputDir: restored, Compression: compression}); err == nil {
					t.Fatal("restore succeeded without reading any encoded data")
				}
				if _, err := os.Stat(restored); !os.IsNotExist(err) {
					t.Fatalf("empty collection created a restore destination: %v", err)
				}
			}
		})
	}
}
