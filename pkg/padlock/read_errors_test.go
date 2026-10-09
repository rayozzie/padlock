// Copyright 2025 Ray Ozzie. All rights reserved.

package padlock

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rayozzie/padlock/pkg/file"
	"github.com/rayozzie/padlock/pkg/pad"
)

type sourceReadFailure struct{ err error }

func (r sourceReadFailure) Read([]byte) (int, error) { return 0, r.err }

type discardReadErrorChunk struct{ io.Writer }

func (discardReadErrorChunk) Close() error { return nil }

func TestEncoderRejectsCompressedSourceErrors(t *testing.T) {
	for _, failure := range []error{errors.New("source read failed"), io.ErrUnexpectedEOF} {
		t.Run(failure.Error(), func(t *testing.T) {
			ctx := context.Background()
			encoder, err := pad.NewPadForEncode(ctx, 2, 2)
			if err != nil {
				t.Fatal(err)
			}
			source := io.MultiReader(bytes.NewReader([]byte("source data before failure")), sourceReadFailure{failure})
			compressed := file.CompressStreamToStream(ctx, source)
			if closer, ok := compressed.(io.Closer); ok {
				defer closer.Close()
			}
			newChunk := func(string, int, string) (io.WriteCloser, error) {
				return discardReadErrorChunk{io.Discard}, nil
			}
			err = encoder.Encode(ctx, 1024, compressed, pad.NewCryptoRand(), newChunk, "bin")
			if !errors.Is(err, failure) {
				t.Fatalf("encoder returned %v, want original source error %v", err, failure)
			}
		})
	}
}

func TestEncodeRejectsUnreadableSource(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix permissions enforced for a non-root user")
	}
	for _, tc := range []struct {
		name        string
		format      Format
		archive     bool
		compression Compression
	}{
		{"png_tar", FormatPNG, true, CompressionGzip},
		{"png_files", FormatPNG, false, CompressionGzip},
		{"bin_tar", FormatBin, true, CompressionGzip},
		{"bin_files", FormatBin, false, CompressionGzip},
		{"uncompressed", FormatPNG, true, CompressionNone},
	} {
		for _, kind := range []string{"file", "directory"} {
			t.Run(tc.name+"/"+kind, func(t *testing.T) {
				cfg := outputErrorConfig(t)
				cfg.Format, cfg.ArchiveCollections, cfg.Compression = tc.format, tc.archive, tc.compression
				cfg.ClearIfNotEmpty = true
				if err := os.Mkdir(cfg.OutputDir, 0700); err != nil {
					t.Fatal(err)
				}
				marker := filepath.Join(cfg.OutputDir, "keep.txt")
				if err := os.WriteFile(marker, []byte("previous backup"), 0600); err != nil {
					t.Fatal(err)
				}
				// This sorts after source.txt: preflight must check the whole tree
				// before preparing output, even after finding readable input.
				unreadable := filepath.Join(cfg.InputDir, "z_unreadable")
				mode := os.FileMode(0600)
				if kind == "directory" {
					mode = 0700
					if err := os.Mkdir(unreadable, mode); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(unreadable, "lost.txt"), []byte("must be included"), 0600); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(unreadable, []byte("must be included"), mode); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(unreadable, 0000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(unreadable, mode) })
				err := EncodeDirectory(context.Background(), cfg)
				if !errors.Is(err, os.ErrPermission) {
					t.Fatalf("expected source permission error, got %v", err)
				}
				if !strings.Contains(err.Error(), unreadable) {
					t.Errorf("source path missing from error: %v", err)
				}
				if data, err := os.ReadFile(marker); err != nil || string(data) != "previous backup" {
					t.Errorf("source preflight cleared existing output: %q, %v", data, err)
				}
				if entries, err := os.ReadDir(cfg.OutputDir); err != nil || len(entries) != 1 {
					t.Errorf("source preflight wrote output: %v, %v", entries, err)
				}
			})
		}
	}
}
