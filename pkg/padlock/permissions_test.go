// Copyright 2025 Ray Ozzie. All rights reserved.

package padlock

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/rayozzie/padlock/pkg/pad"
)

func TestEncodePrivateCollections(t *testing.T) {
	ctx := context.Background()
	for _, format := range []Format{FormatBin, FormatPNG} {
		for _, archive := range []bool{false, true} {
			for _, multiple := range []bool{false, true} {
				name := fmt.Sprintf("%s/archive=%t/multiple=%t", format, archive, multiple)
				t.Run(name, func(t *testing.T) {
					base := t.TempDir()
					input := filepath.Join(base, "input")
					if err := os.Mkdir(input, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(input, "secret.txt"), []byte("owner-only source data"), 0600); err != nil {
						t.Fatal(err)
					}
					outputs := []string{filepath.Join(base, "output")}
					cfg := EncodeConfig{
						InputDir:           input,
						OutputDir:          outputs[0],
						N:                  2,
						K:                  2,
						Format:             format,
						ChunkSize:          1024,
						RNG:                pad.NewDefaultRand(ctx),
						Compression:        CompressionGzip,
						ArchiveCollections: archive,
					}
					if multiple {
						outputs = append(outputs, filepath.Join(base, "output2"))
						cfg.OutputDirs = outputs
					}
					if err := EncodeDirectory(ctx, cfg); err != nil {
						t.Fatal(err)
					}
					for _, output := range outputs {
						files := 0
						err := filepath.WalkDir(output, func(path string, entry fs.DirEntry, err error) error {
							if err != nil {
								return err
							}
							want := os.FileMode(0600)
							if entry.IsDir() {
								want = 0700
							} else {
								files++
							}
							info, err := entry.Info()
							if err != nil {
								return err
							}
							if !entry.IsDir() && info.Size() == 0 {
								t.Errorf("empty collection file: %s", path)
							}
							if runtime.GOOS != "windows" && info.Mode().Perm() != want {
								t.Errorf("%s permissions = %04o, want %04o", path, info.Mode().Perm(), want)
							}
							return nil
						})
						if err != nil {
							t.Fatal(err)
						}
						if files == 0 {
							t.Errorf("no collection files created in %s", output)
						}
					}
				})
			}
		}
	}
}
