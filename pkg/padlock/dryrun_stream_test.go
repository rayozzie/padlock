// Copyright 2025 Ray Ozzie. All rights reserved.

package padlock

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/rayozzie/padlock/pkg/file"
)

// Serialization and compression can still log while error cleanup finishes.
type dryRunLog struct {
	sync.Mutex
	buffer bytes.Buffer
}

func (l *dryRunLog) Write(p []byte) (int, error) {
	l.Lock()
	defer l.Unlock()
	return l.buffer.Write(p)
}

func (l *dryRunLog) String() string {
	l.Lock()
	defer l.Unlock()
	return l.buffer.String()
}

// These tests are intentionally sequential: the production tracer uses log's
// global writer. Restore the original writer, including on a test failure.
func captureDryRunLog(t *testing.T) *dryRunLog {
	t.Helper()
	captured := new(dryRunLog)
	previous := log.Writer()
	log.SetOutput(captured)
	t.Cleanup(func() { log.SetOutput(previous) })
	return captured
}

func writeDryRunInput(t *testing.T, path string, size int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// Deterministic, effectively incompressible input, generated with a small
	// copy buffer so the fixture itself does not consume input-sized memory.
	_, copyErr := io.CopyN(f, rand.New(rand.NewSource(1)), size)
	if err := errors.Join(copyErr, f.Close()); err != nil {
		t.Fatal(err)
	}
}

func TestDryRunStreamSizes(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"empty", "compressible", "incompressible"} {
		for _, compression := range []Compression{CompressionNone, CompressionGzip} {
			for _, format := range []Format{FormatBin, FormatPNG} {
				for _, archive := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/gzip=%t/%s/tar=%t", kind, compression == CompressionGzip, format, archive), func(t *testing.T) {
						cfg := outputErrorConfig(t)
						cfg.SizeOnly, cfg.ClearIfNotEmpty = true, true
						cfg.Compression, cfg.Format, cfg.ArchiveCollections = compression, format, archive
						cfg.N, cfg.K, cfg.ChunkSize = 3, 2, 4096
						source := filepath.Join(cfg.InputDir, "source.txt")
						switch kind {
						case "empty":
							if err := os.Remove(source); err != nil {
								t.Fatal(err)
							}
						case "compressible":
							if err := os.WriteFile(source, bytes.Repeat([]byte("compressible\n"), 10000), 0600); err != nil {
								t.Fatal(err)
							}
						case "incompressible":
							writeDryRunInput(t, source, 128*1024+19)
						}
						// Buffer only this small test fixture to establish exact counts
						// independently of the production counting readers.
						stream, err := file.SerializeDirectoryToStream(ctx, cfg.InputDir)
						if err != nil {
							t.Fatal(err)
						}
						serialized, err := io.ReadAll(stream)
						closeErr := stream.Close()
						if err := errors.Join(err, closeErr); err != nil {
							t.Fatal(err)
						}
						var compressed bytes.Buffer
						zw := gzip.NewWriter(&compressed)
						if _, err := zw.Write(serialized); err != nil {
							t.Fatal(err)
						}
						if err := zw.Close(); err != nil {
							t.Fatal(err)
						}
						// Include an existing and an absent output. Neither may change,
						// even with -clear and multiple destinations configured.
						if err := os.Mkdir(cfg.OutputDir, 0700); err != nil {
							t.Fatal(err)
						}
						marker := filepath.Join(cfg.OutputDir, "keep")
						if err := os.WriteFile(marker, []byte("previous backup"), 0600); err != nil {
							t.Fatal(err)
						}
						cfg.OutputDirs = []string{cfg.OutputDir, cfg.OutputDir + "-absent", cfg.OutputDir + "-also-absent"}
						before := snapshotDirectoryTree(t, filepath.Dir(cfg.InputDir))
						captured := captureDryRunLog(t)
						if err := EncodeDirectory(ctx, cfg); err != nil {
							t.Fatal(err)
						}
						output := captured.String()
						want := fmt.Sprintf("Original input size:              %s bytes", FormatByteSize(int64(len(serialized))))
						if !strings.Contains(output, want) {
							t.Errorf("missing %q in report:\n%s", want, output)
						}
						if compression == CompressionGzip {
							want = fmt.Sprintf("Compressed input size:            %s bytes", FormatByteSize(int64(compressed.Len())))
							if !strings.Contains(output, want) {
								t.Errorf("missing %q in report:\n%s", want, output)
							}
						} else if strings.Contains(output, "Compressed input size:") {
							t.Error("uncompressed dry run reported compression")
						}
						if after := snapshotDirectoryTree(t, filepath.Dir(cfg.InputDir)); !maps.Equal(before, after) {
							t.Error("dry run modified the input or outputs")
						}
					})
				}
			}
		}
	}
}

func TestDryRunStreamsBeforeSourceEOF(t *testing.T) {
	for _, compression := range []Compression{CompressionNone, CompressionGzip} {
		t.Run(fmt.Sprintf("gzip=%t", compression == CompressionGzip), func(t *testing.T) {
			cfg := outputErrorConfig(t)
			cfg.SizeOnly, cfg.Compression, cfg.ChunkSize = true, compression, 4096
			source := filepath.Join(cfg.InputDir, "source.txt")
			writeDryRunInput(t, source, 1024*1024)
			cfg.RNG = &encodeFaultRNG{RNG: cfg.RNG, fault: func(read int) error {
				if read == 1 {
					// Encoding must start before serialization has read this whole
					// file. Truncating it now must cause a later source failure.
					return os.Truncate(source, 0)
				}
				return nil
			}}
			captured := captureDryRunLog(t)
			if err := EncodeDirectory(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "archive/tar: missed writing") {
				t.Fatalf("expected incomplete source error during streaming, got %v", err)
			}
			if output := captured.String(); strings.Contains(output, "DRY RUN SIZE REPORT") || strings.Contains(output, "Encode complete") {
				t.Fatalf("failed source produced a success report:\n%s", output)
			}
			if _, err := os.Stat(cfg.OutputDir); !os.IsNotExist(err) {
				t.Fatalf("dry run created output: %v", err)
			}
		})
	}
}

func TestDryRunStreamingFailures(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancel), func(t *testing.T) {
			cfg := outputErrorConfig(t)
			cfg.SizeOnly, cfg.Compression, cfg.ChunkSize = true, CompressionGzip, 4096
			writeDryRunInput(t, filepath.Join(cfg.InputDir, "source.txt"), 1024*1024)
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			failure := errors.New("injected RNG failure")
			if cancel {
				failure = context.Canceled
			}
			cfg.RNG = &encodeFaultRNG{RNG: cfg.RNG, fault: func(int) error {
				if cancel {
					stop()
				}
				return failure
			}}
			captured := captureDryRunLog(t)
			if err := EncodeDirectory(ctx, cfg); !errors.Is(err, failure) {
				t.Fatalf("expected %v, got %v", failure, err)
			}
			if output := captured.String(); strings.Contains(output, "DRY RUN SIZE REPORT") || strings.Contains(output, "Encode complete") {
				t.Fatalf("failed encode produced a success report:\n%s", output)
			}
			if _, err := os.Stat(cfg.OutputDir); !os.IsNotExist(err) {
				t.Fatalf("dry run created output: %v", err)
			}
		})
	}
}

func TestDryRunBoundedRetainedMemory(t *testing.T) {
	for _, compression := range []Compression{CompressionNone, CompressionGzip} {
		t.Run(fmt.Sprintf("gzip=%t", compression == CompressionGzip), func(t *testing.T) {
			cfg := outputErrorConfig(t)
			cfg.SizeOnly, cfg.Compression = true, compression
			cfg.N, cfg.K, cfg.ChunkSize = 3, 2, 2*1024*1024
			writeDryRunInput(t, filepath.Join(cfg.InputDir, "source.txt"), 64*1024*1024)
			captureDryRunLog(t)
			runtime.GC()
			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			var peak, samples uint64
			cfg.RNG = &encodeFaultRNG{RNG: cfg.RNG, fault: func(read int) error {
				if read != 1 && read%16 != 0 {
					return nil
				}
				// Sample while encoding is active. TotalAlloc measures cumulative
				// allocation, not simultaneous memory: reclaimed chunk buffers
				// legitimately make it grow with the amount of data processed.
				runtime.GC()
				var current runtime.MemStats
				runtime.ReadMemStats(&current)
				samples++
				if current.HeapAlloc > before.HeapAlloc && current.HeapAlloc-before.HeapAlloc > peak {
					peak = current.HeapAlloc - before.HeapAlloc
				}
				if peak > 32*1024*1024 {
					return fmt.Errorf("dry run retained %d extra heap bytes; limit is 32 MiB for a 64 MiB input", peak)
				}
				return nil
			}}
			if err := EncodeDirectory(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}
			if samples < 2 {
				t.Fatalf("expected memory samples across multiple chunks, got %d", samples)
			}
			t.Logf("peak sampled retained heap increase: %d bytes (%d samples)", peak, samples)
			if _, err := os.Stat(cfg.OutputDir); !os.IsNotExist(err) {
				t.Fatalf("dry run created output: %v", err)
			}
		})
	}
}
