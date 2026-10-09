// Copyright 2025 Ray Ozzie. All rights reserved.

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInvalidChunkSizeCLI(t *testing.T) {
	for _, tc := range []struct {
		name        string
		outputCount int
		flags       []string
		minimum     int
	}{
		{"negative", 1, []string{"-chunk", "-1"}, 1},
		{"zero", 1, []string{"-chunk", "0"}, 1},
		{"two_of_three", 1, []string{"-copies", "3", "-chunk", "1"}, 2},
		{"three_of_five", 1, []string{"-copies", "5", "-required", "3", "-chunk", "5"}, 6},
		{"default_too_small", 1, []string{"-copies", "26", "-required", "13"}, 5200300},
		{"multiple_outputs", 3, []string{"-required", "2", "-chunk", "1"}, 2},
		{"dry_run", 0, []string{"-copies", "5", "-required", "3", "-chunk", "5", "-dryrun"}, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, outputs, source := cliFixture(t, tc.outputCount)
			for i, dir := range outputs {
				if i%2 == 0 {
					if err := os.Mkdir(dir, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("existing backup"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			args := append([]string{"encode", input}, outputs...)
			args = append(args, "-clear", "-format", "bin")
			args = append(args, tc.flags...)
			output, err := runCLI(t, args...)
			if err == nil || !strings.Contains(output, fmt.Sprintf("chunk size must be at least %d", tc.minimum)) || strings.Contains(output, "panic:") {
				t.Errorf("expected a normal chunk size error, got %v:\n%s", err, output)
			}
			for i, dir := range outputs {
				if i%2 != 0 {
					if _, err := os.Stat(dir); !os.IsNotExist(err) {
						t.Errorf("invalid chunk size created output directory %s: %v", dir, err)
					}
					continue
				}
				data, err := os.ReadFile(filepath.Join(dir, "keep.txt"))
				if err != nil || string(data) != "existing backup" {
					t.Errorf("invalid chunk size changed existing output: %q, err=%v", data, err)
				}
				entries, err := os.ReadDir(dir)
				if err != nil || len(entries) != 1 {
					t.Errorf("invalid chunk size changed output entries: %v, err=%v", entries, err)
				}
			}
			got, err := os.ReadFile(filepath.Join(input, "source.bin"))
			if err != nil || !bytes.Equal(got, source) {
				t.Fatalf("invalid chunk size changed source data: %v", err)
			}
		})
	}
}

func TestMinimumChunkSizeCLI(t *testing.T) {
	// With three output directories and no -required, the effective threshold
	// is 3-of-3, whose minimum chunk size is one byte.
	input, outputs, _ := cliFixture(t, 3)
	args := append([]string{"encode", input}, outputs...)
	args = append(args, "-chunk", "1", "-dryrun", "-format", "bin")
	if output, err := runCLI(t, args...); err != nil {
		t.Fatalf("valid minimum chunk size was rejected: %v\n%s", err, output)
	}
	for _, dir := range outputs {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("dry run created output directory %s: %v", dir, err)
		}
	}
}
