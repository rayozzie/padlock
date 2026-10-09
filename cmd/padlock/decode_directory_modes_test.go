// Copyright 2025 Ray Ozzie. All rights reserved.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReadOnlyDirectoryRoundTripCLI(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix directory permissions enforced for a non-root user")
	}
	for _, format := range []string{"bin", "png"} {
		for _, archive := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/tar=%t", format, archive), func(t *testing.T) {
				base := t.TempDir()
				input, encoded, restored := filepath.Join(base, "input"), filepath.Join(base, "encoded"), filepath.Join(base, "restored")
				directories := []struct {
					name string
					mode os.FileMode
				}{
					{"locked", 0555},
					{"locked/nested", 0500},
					{"locked/empty", 0555},
				}
				t.Cleanup(func() {
					// Re-enable parent access before cleaning up read-only fixtures.
					for _, root := range []string{input, restored} {
						for _, dir := range directories {
							_ = os.Chmod(filepath.Join(root, dir.name), 0700)
						}
					}
				})
				for _, dir := range directories {
					if err := os.MkdirAll(filepath.Join(input, dir.name), 0700); err != nil {
						t.Fatal(err)
					}
				}
				files := map[string]string{
					"locked/nested/data.txt": "data inside a read-only nested directory\n",
					"locked/other.txt":       "another file in the read-only parent\n",
				}
				for name, contents := range files {
					path := filepath.Join(input, name)
					if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(path, 0400); err != nil {
						t.Fatal(err)
					}
				}
				for _, dir := range directories {
					if err := os.Chmod(filepath.Join(input, dir.name), dir.mode); err != nil {
						t.Fatal(err)
					}
				}
				args := []string{"encode", input, encoded, "-format", format}
				if !archive {
					args = append(args, "-files")
				}
				if output, err := runCLI(t, args...); err != nil {
					t.Fatalf("encode failed: %v:\n%s", err, output)
				}
				if output, err := runCLI(t, "decode", encoded, restored); err != nil {
					t.Fatalf("read-only directory could not be restored: %v:\n%s", err, output)
				}
				for name, want := range files {
					path := filepath.Join(restored, name)
					got, err := os.ReadFile(path)
					if err != nil || string(got) != want {
						t.Errorf("restored %s = %q, want %q; err=%v", name, got, want, err)
					}
					info, err := os.Stat(path)
					if err != nil || info.Mode().Perm() != 0400 {
						t.Errorf("read-only file mode was not preserved for %s: %v; err=%v", name, info, err)
					}
				}
				for _, dir := range directories {
					info, err := os.Stat(filepath.Join(restored, dir.name))
					if err != nil || info.Mode().Perm() != dir.mode {
						t.Errorf("restored mode for %s = %v, want %04o; err=%v", dir.name, info, dir.mode, err)
					}
				}
			})
		}
	}
}
