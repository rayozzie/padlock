// Copyright 2025 Ray Ozzie. All rights reserved.

package padlock

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/rayozzie/padlock/pkg/file"
	"github.com/rayozzie/padlock/pkg/pad"
)

// Run a fault after directory preparation, at a deterministic point in encoding.
type encodeFaultRNG struct {
	pad.RNG
	reads int
	fault func(int) error
}

func (r *encodeFaultRNG) Read(ctx context.Context, p []byte) error {
	r.reads++
	if err := r.fault(r.reads); err != nil {
		return err
	}
	return r.RNG.Read(ctx, p)
}

func outputErrorConfig(t *testing.T) EncodeConfig {
	t.Helper()
	base := t.TempDir()
	input := filepath.Join(base, "input")
	if err := os.Mkdir(input, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(input, "source.txt"), []byte("data that must be saved"), 0600); err != nil {
		t.Fatal(err)
	}
	return EncodeConfig{
		InputDir: input, OutputDir: filepath.Join(base, "output"),
		N: 2, K: 2, Format: FormatPNG, ChunkSize: 512,
		Compression: CompressionNone, RNG: pad.NewCryptoRand(),
	}
}

func TestEncodeDirectoryReturnsChunkWriteFailure(t *testing.T) {
	for _, format := range []Format{FormatPNG, FormatBin} {
		t.Run(string(format), func(t *testing.T) {
			cfg := outputErrorConfig(t)
			cfg.Format = format
			cfg.Compression = CompressionGzip
			cfg.RNG = &encodeFaultRNG{RNG: cfg.RNG, fault: func(read int) error {
				if read != 1 {
					return nil
				}
				name := "2A2_0001.bin"
				if format == FormatPNG {
					name = "IMG2A2_0001.PNG"
				}
				// An occupied output path reliably fails file creation, even as root.
				return os.Mkdir(filepath.Join(cfg.OutputDir, "2A2", name), 0700)
			}}
			if err := EncodeDirectory(context.Background(), cfg); !errors.Is(err, os.ErrExist) {
				t.Fatalf("expected file creation failure, got %v", err)
			}
		})
	}
}

func TestEncodeDirectoryReturnsVerificationFailure(t *testing.T) {
	cfg := outputErrorConfig(t)
	cfg.RNG = &encodeFaultRNG{RNG: cfg.RNG, fault: func(read int) error {
		if read != 2 {
			return nil
		}
		// Damage a completed chunk while subsequent chunks are being encoded.
		return os.WriteFile(filepath.Join(cfg.OutputDir, "2A2", "IMG2A2_0001.PNG"), []byte("damaged PNG"), 0600)
	}}
	if err := EncodeDirectory(context.Background(), cfg); err == nil {
		t.Fatal("encoding reported success despite failed verification")
	}
}

func TestFailedEncodeClosesArchives(t *testing.T) {
	ctx := context.Background()
	cfg := outputErrorConfig(t)
	cfg.ArchiveCollections = true
	failure := errors.New("injected failure after the first chunk")
	cfg.RNG = &encodeFaultRNG{RNG: cfg.RNG, fault: func(read int) error {
		if read == 2 {
			return failure
		}
		return nil
	}}
	t.Cleanup(func() { _ = file.FinalizeAllTarWriters(ctx) })
	if err := EncodeDirectory(ctx, cfg); !errors.Is(err, failure) {
		t.Fatalf("expected encoding failure, got %v", err)
	}
	for _, name := range []string{"2A2", "2B2"} {
		path := filepath.Join(cfg.OutputDir, name+".tar")
		// A stale cached writer would be returned here if error cleanup was missed.
		if _, err := file.NewTarChunkWriter(ctx, path, name, file.FormatPNG); !errors.Is(err, os.ErrExist) {
			t.Errorf("archive writer %s remained cached after failure: %v", name, err)
		}
	}
}

func verificationCollection(t *testing.T, name string, archive bool, empty bool) file.Collection {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if !empty {
		if err := file.WriteNamedChunk(context.Background(), &file.PngFormatter{}, dir, name, 1, []byte("test chunk")); err != nil {
			t.Fatal(err)
		}
	}
	path := dir
	if archive {
		var err error
		path, err = file.TarCollection(context.Background(), dir)
		if err != nil {
			t.Fatal(err)
		}
	}
	return file.Collection{Name: name, Path: path, Format: file.FormatPNG}
}

func TestVerifyRejectsMissingAndEmptyCollections(t *testing.T) {
	ctx := context.Background()
	if err := VerifyCollectionIntegrity(ctx, nil, FormatPNG); err == nil {
		t.Error("verification accepted no collections")
	}
	for _, archive := range []bool{false, true} {
		storage := "directory"
		if archive {
			storage = "tar"
		}
		for _, state := range []string{"valid", "empty", "missing"} {
			t.Run(storage+"/"+state, func(t *testing.T) {
				good := verificationCollection(t, "2A2", archive, false)
				other := verificationCollection(t, "2B2", archive, state == "empty")
				if state == "missing" {
					if err := os.RemoveAll(other.Path); err != nil {
						t.Fatal(err)
					}
				}
				err := VerifyCollectionIntegrity(ctx, []file.Collection{good, other}, FormatPNG)
				if state == "valid" && err != nil {
					t.Fatal(err)
				}
				if state != "valid" && err == nil {
					t.Fatalf("verification accepted a %s collection alongside a valid one", state)
				}
			})
		}
	}
}

func TestVerifyDirectoryWithGlobCharacters(t *testing.T) {
	collection := verificationCollection(t, "backup[1]", false, false)
	if err := VerifyCollectionIntegrity(context.Background(), []file.Collection{collection}, FormatPNG); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRejectsUnreadableCollections(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix permissions enforced for a non-root user")
	}
	for _, archive := range []bool{false, true} {
		name := "directory"
		mode := os.FileMode(0700)
		if archive {
			name, mode = "tar", 0600
		}
		t.Run(name, func(t *testing.T) {
			good := verificationCollection(t, "2A2", archive, false)
			unreadable := verificationCollection(t, "2B2", archive, false)
			if err := os.Chmod(unreadable.Path, 0000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(unreadable.Path, mode) })
			err := VerifyCollectionIntegrity(context.Background(), []file.Collection{good, unreadable}, FormatPNG)
			if !errors.Is(err, os.ErrPermission) {
				t.Fatalf("expected unreadable collection error, got %v", err)
			}
		})
	}
}

func TestVerifyStopsAtDamagedTarHeader(t *testing.T) {
	collection := verificationCollection(t, "2A2", true, false)
	data, err := os.ReadFile(collection.Path)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the valid first entry, then replace the end markers with a bad header.
	data = append(data[:len(data)-1024], bytes.Repeat([]byte{0xff}, 512)...)
	if err := os.WriteFile(collection.Path, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = VerifyCollectionIntegrity(ctx, []file.Collection{collection}, FormatPNG)
	if err == nil {
		t.Fatal("verification accepted a damaged TAR")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("verification kept retrying the invalid TAR header")
	}
}

func TestEmptySourceDirectoryStillEncodes(t *testing.T) {
	for _, archive := range []bool{false, true} {
		name := "files"
		if archive {
			name = "tar"
		}
		t.Run(name, func(t *testing.T) {
			cfg := outputErrorConfig(t)
			cfg.ArchiveCollections = archive
			if err := os.Remove(filepath.Join(cfg.InputDir, "source.txt")); err != nil {
				t.Fatal(err)
			}
			if err := EncodeDirectory(context.Background(), cfg); err != nil {
				t.Fatalf("an empty source still produces TAR data to encode: %v", err)
			}
		})
	}
}
