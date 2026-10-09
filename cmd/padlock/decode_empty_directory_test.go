// Copyright 2025 Ray Ozzie. All rights reserved.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEmptyDirectoryRoundTripCLI(t *testing.T) {
	for _, directoriesOnly := range []bool{false, true} {
		for _, format := range []string{"bin", "png"} {
			for _, archive := range []bool{false, true} {
				t.Run(fmt.Sprintf("directories=%t/%s/tar=%t", directoriesOnly, format, archive), func(t *testing.T) {
					root := t.TempDir()
					input, encoded, restored := filepath.Join(root, "input"), filepath.Join(root, "encoded"), filepath.Join(root, "restored")
					if err := os.Mkdir(input, 0700); err != nil {
						t.Fatal(err)
					}
					var names []string
					if directoriesOnly {
						names = []string{"parent", "parent/child", "sibling"}
					}
					t.Cleanup(func() {
						// Restore parent access before removing read-only fixtures.
						for _, base := range []string{input, restored} {
							for _, name := range names {
								_ = os.Chmod(filepath.Join(base, name), 0700)
							}
						}
					})
					for _, name := range names {
						if err := os.MkdirAll(filepath.Join(input, name), 0700); err != nil {
							t.Fatal(err)
						}
					}
					if runtime.GOOS != "windows" {
						for _, name := range names {
							if err := os.Chmod(filepath.Join(input, name), 0555); err != nil {
								t.Fatal(err)
							}
						}
					}
					args := []string{"encode", input, encoded, "-format", format, "-copies", "3", "-required", "2"}
					if !archive {
						args = append(args, "-files")
					}
					if output, err := runCLI(t, args...); err != nil {
						t.Fatalf("empty/directory-only encode failed: %v\n%s", err, output)
					}
					// Restore from exactly the threshold number of collections.
					unused := filepath.Join(encoded, "2B3")
					if archive {
						unused += ".tar"
					}
					if err := os.RemoveAll(unused); err != nil {
						t.Fatal(err)
					}
					output, err := runCLI(t, "decode", encoded, restored, "-dryrun")
					if err != nil || !strings.Contains(output, "DRY RUN SIZE REPORT") {
						t.Fatalf("empty/directory-only dry run failed: %v\n%s", err, output)
					}
					if _, err := os.Stat(restored); !os.IsNotExist(err) {
						t.Fatalf("dry run created output: %v", err)
					}
					// A valid empty backup also restores over an existing destination
					// when -clear is requested, leaving no placeholder files behind.
					if !directoriesOnly {
						if err := os.Mkdir(restored, 0700); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(restored, "old.txt"), []byte("old contents"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					if output, err := runCLI(t, "decode", encoded, restored, "-clear"); err != nil || !strings.Contains(output, "Decode complete") {
						t.Fatalf("empty/directory-only restore failed: %v\n%s", err, output)
					}
					count := 0
					if err := filepath.Walk(restored, func(path string, info os.FileInfo, err error) error {
						if err != nil {
							return err
						}
						if !info.IsDir() {
							return fmt.Errorf("unexpected restored file: %s", path)
						}
						if path != restored {
							count++
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
					if count != len(names) {
						t.Fatalf("restored %d directories, want %d", count, len(names))
					}
					for _, name := range names {
						info, err := os.Stat(filepath.Join(restored, name))
						if err != nil || !info.IsDir() {
							t.Fatalf("missing restored directory %s: %v", name, err)
						}
						if runtime.GOOS != "windows" && info.Mode().Perm() != 0555 {
							t.Fatalf("directory %s mode = %04o, want 0555", name, info.Mode().Perm())
						}
					}
				})
			}
		}
	}
}
