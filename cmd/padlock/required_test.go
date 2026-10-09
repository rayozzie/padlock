// Copyright 2025 Ray Ozzie. All rights reserved.

package main

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestInvalidRequiredLeavesOutputsUntouchedCLI(t *testing.T) {
	minInt := -int(^uint(0)>>1) - 1
	for _, flag := range []struct {
		name     string
		args     []string
		required int
	}{
		{"one", []string{"-required", "1"}, 1},
		{"equals_one", []string{"-required=1"}, 1},
		{"double_dash_one", []string{"--required=1"}, 1},
		{"zero", []string{"-required", "0"}, 0},
		{"negative", []string{"-required", "-1"}, -1},
		{"minimum_int", []string{"-required=" + strconv.Itoa(minInt)}, minInt},
		{"last_value_one", []string{"-required", "3", "-required", "1"}, 1},
		{"above_copies", []string{"-required", "4"}, 4},
	} {
		for _, layout := range []struct {
			name     string
			outputs  int
			existing bool
			files    bool
		}{
			{"single_existing", 1, true, false},
			{"single_missing", 1, false, true},
			{"multiple", 3, true, false},
			{"no_output", 0, false, false},
		} {
			for _, dryRun := range []bool{false, true} {
				if layout.outputs == 0 && !dryRun {
					continue
				}
				t.Run(fmt.Sprintf("%s/%s/dry=%t", flag.name, layout.name, dryRun), func(t *testing.T) {
					input, outputs, _ := cliFixture(t, layout.outputs)
					root := filepath.Dir(input)
					if layout.existing {
						for i, dir := range outputs {
							if i%2 != 0 {
								continue // Preserve missing destinations as well.
							}
							if err := os.Mkdir(dir, 0700); err != nil {
								t.Fatal(err)
							}
							if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("previous backup"), 0600); err != nil {
								t.Fatal(err)
							}
						}
					}
					before := snapshotOutputLayoutCLI(t, root)
					args := append([]string{"encode", input}, outputs...)
					args = append(args, "-copies", "3", "-clear", "-format", "bin")
					args = append(args, flag.args...)
					if dryRun {
						args = append(args, "-dryrun")
					}
					if layout.files {
						args = append(args, "-files")
					}
					output, err := runCLI(t, args...)
					message := fmt.Sprintf("-required value %d must be at least 2", flag.required)
					if flag.required > 3 {
						message = "-required value 4 cannot be greater than number of collections (-copies) 3"
					}
					var exitErr *exec.ExitError
					if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || !strings.Contains(output, message) {
						t.Errorf("expected invalid-threshold error %q, got %v:\n%s", message, err, output)
					}
					for _, misleading := range []string{"using minimum value", "Encode complete", "DRY RUN SIZE REPORT", "panic:"} {
						if strings.Contains(output, misleading) {
							t.Errorf("invalid threshold reported %q:\n%s", misleading, output)
						}
					}
					if after := snapshotOutputLayoutCLI(t, root); !maps.Equal(before, after) {
						t.Error("invalid threshold changed the source or output layout")
					}
				})
			}
		}
	}
}

func TestInvalidRequiredPrecedesEntropySetupCLI(t *testing.T) {
	input, outputs, _ := cliFixture(t, 1)
	missingEntropy := filepath.Join(filepath.Dir(input), "missing-entropy")
	output, err := runCLI(t, "encode", input, outputs[0], "-required", "1", "-entropy-file", missingEntropy)
	if err == nil || !strings.Contains(output, "-required value 1 must be at least 2") || strings.Contains(output, "entropy setup failed") {
		t.Fatalf("invalid threshold reached entropy setup: %v\n%s", err, output)
	}
}
