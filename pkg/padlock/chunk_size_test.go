// Copyright 2025 Ray Ozzie. All rights reserved.

package padlock

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInvalidChunkSizeLeavesDirectoriesUntouched(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		total, required, chunk, minimum int
	}{
		{"negative", 2, 2, -1, 1},
		{"zero", 2, 2, 0, 1},
		{"two_of_three", 3, 2, 1, 2},
		{"three_of_five", 5, 3, 5, 6},
		{"default_too_small", 26, 13, 2 * 1024 * 1024, 5200300},
	} {
		for _, layout := range []string{"existing", "new", "multiple"} {
			for _, clear := range []bool{false, true} {
				for _, dryRun := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/clear=%t/dryrun=%t", tc.name, layout, clear, dryRun), func(t *testing.T) {
						root := t.TempDir()
						input, output := filepath.Join(root, "input"), filepath.Join(root, "output")
						writePathFixture(t, filepath.Join(input, "source.txt"), "irreplaceable source data")
						if layout != "new" {
							writePathFixture(t, filepath.Join(output, "nested", "keep.txt"), "existing backup")
						}
						rng := &encodeFaultRNG{fault: func(int) error { return errors.New("encoding started with invalid settings") }}
						cfg := EncodeConfig{
							InputDir: input, OutputDir: output, N: tc.total, K: tc.required,
							Format: FormatBin, ChunkSize: tc.chunk, RNG: rng,
							ClearIfNotEmpty: clear, SizeOnly: dryRun, ArchiveCollections: true, Compression: CompressionGzip,
						}
						if layout == "multiple" {
							cfg.OutputDirs = []string{output}
							for i := 1; i < tc.total; i++ {
								cfg.OutputDirs = append(cfg.OutputDirs, filepath.Join(root, fmt.Sprintf("output%d", i)))
							}
							writePathFixture(t, filepath.Join(cfg.OutputDirs[tc.total-1], "keep.txt"), "another existing backup")
						}
						before := snapshotDirectoryTree(t, root)
						err := EncodeDirectory(context.Background(), cfg)
						if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("chunk size must be at least %d", tc.minimum)) {
							t.Errorf("got %v, want chunk size validation before directory preparation", err)
						}
						if after := snapshotDirectoryTree(t, root); !reflect.DeepEqual(after, before) {
							t.Fatal("invalid chunk size changed source or output directories")
						}
						if rng.reads != 0 {
							t.Fatal("invalid chunk size started encoding")
						}
					})
				}
			}
		}
	}
}
