// Copyright 2025 Ray Ozzie. All rights reserved.

package padlock

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"strings"
	"testing"
)

func encodeDryRunReport(t *testing.T, cfg EncodeConfig) string {
	t.Helper()
	captured := captureDryRunLog(t)
	if err := EncodeDirectory(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	var report []string
	inReport := false
	for _, line := range strings.Split(captured.String(), "\n") {
		_, message, found := strings.Cut(line, "padlock: ")
		if !found {
			continue
		}
		if message == "*** DRY RUN SIZE REPORT ***" {
			inReport = true
		} else if inReport {
			if message == "***" {
				return strings.Join(report, "\n")
			}
			report = append(report, message)
		}
	}
	t.Fatalf("missing complete size report:\n%s", captured.String())
	return ""
}

func TestDryRunRootSymlinkSizes(t *testing.T) {
	for _, compression := range []Compression{CompressionNone, CompressionGzip} {
		for _, format := range []Format{FormatBin, FormatPNG} {
			for _, archive := range []bool{false, true} {
				t.Run(fmt.Sprintf("gzip=%t/%s/tar=%t", compression == CompressionGzip, format, archive), func(t *testing.T) {
					cfg := outputErrorConfig(t)
					cfg.SizeOnly, cfg.ClearIfNotEmpty = true, true
					cfg.Compression, cfg.Format, cfg.ArchiveCollections = compression, format, archive
					cfg.N, cfg.K, cfg.ChunkSize = 3, 2, 4096
					writeDryRunInput(t, filepath.Join(cfg.InputDir, "source.txt"), 50*1024)
					root := filepath.Dir(cfg.InputDir)
					alias := filepath.Join(root, "alias")
					linkPathFixture(t, "input", alias)
					// A dry run can overlap its input without preparing/clearing it.
					// Its explicit output destinations must still be distinct.
					cfg.OutputDir = cfg.InputDir
					cfg.OutputDirs = []string{cfg.InputDir, filepath.Join(root, "second"), filepath.Join(root, "absent")}
					before := snapshotDirectoryTree(t, root)
					want := encodeDryRunReport(t, cfg)
					// A 50 KiB file contributes one TAR header and two ending blocks.
					if !strings.Contains(want, "Original input size:              52,736 bytes") {
						t.Fatalf("direct input count is wrong:\n%s", want)
					}
					cfg.InputDir = alias
					if got := encodeDryRunReport(t, cfg); got != want {
						t.Fatalf("symlink dry-run report differs:\n%s\nwant:\n%s", got, want)
					}
					// The library also supports a dry run without output paths.
					cfg.OutputDir, cfg.OutputDirs = "", nil
					if got := encodeDryRunReport(t, cfg); got != want {
						t.Fatalf("output-free dry run differs:\n%s\nwant:\n%s", got, want)
					}
					if after := snapshotDirectoryTree(t, root); !maps.Equal(before, after) {
						t.Fatal("dry run changed an input, link, or output directory")
					}
				})
			}
		}
	}
}
