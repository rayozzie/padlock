// Copyright 2025 Ray Ozzie. All rights reserved.

package main

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func snapshotOutputLayoutCLI(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := info.Mode().String()
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			value += ":" + target
		} else if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += ":" + string(data)
		}
		snapshot[path] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestEncodeOverlappingOutputsBeforeClearCLI(t *testing.T) {
	for _, relation := range []string{"same", "nested", "missing_nested", "alias"} {
		for _, dryRun := range []bool{false, true} {
			for _, files := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/dry=%t/files=%t", relation, dryRun, files), func(t *testing.T) {
					input, outputs, _ := cliFixture(t, 4)
					root := filepath.Dir(input)
					// An invalid later pair must not clear or create earlier outputs.
					for _, dir := range []string{outputs[0], outputs[2]} {
						if relation == "missing_nested" && dir == outputs[2] {
							continue
						}
						if err := os.Mkdir(dir, 0700); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(dir, "keep"), []byte("previous backup"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					switch relation {
					case "same":
						outputs[3] = outputs[2]
					case "nested", "missing_nested":
						outputs[3] = filepath.Join(outputs[2], "child")
					case "alias":
						if err := os.Symlink(outputs[2], outputs[3]); err != nil {
							t.Skipf("symlink unavailable: %v", err)
						}
					}
					before := snapshotOutputLayoutCLI(t, root)
					args := append([]string{"encode", input}, outputs...)
					args = append(args, "-required", "2", "-format", "bin", "-clear")
					if dryRun {
						args = append(args, "-dryrun")
					}
					if files {
						args = append(args, "-files")
					}
					output, err := runCLI(t, args...)
					if err == nil || !strings.Contains(output, "output directories overlap") ||
						!strings.Contains(output, fmt.Sprintf("%q", outputs[2])) || !strings.Contains(output, fmt.Sprintf("%q", outputs[3])) {
						t.Errorf("expected error identifying overlapping outputs: %v\n%s", err, output)
					}
					for _, success := range []string{"Encode complete", "Encoding completed successfully", "DRY RUN SIZE REPORT"} {
						if strings.Contains(output, success) {
							t.Errorf("reported success for overlapping destinations: %s", output)
						}
					}
					if after := snapshotOutputLayoutCLI(t, root); !maps.Equal(before, after) {
						t.Error("rejected layout changed source, destinations, or symlinks")
					}
				})
			}
		}
	}
}
