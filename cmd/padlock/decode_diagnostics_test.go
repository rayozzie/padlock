// Copyright 2025 Ray Ozzie. All rights reserved.

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rayozzie/padlock/pkg/file"
)

func TestDecodeOneCollectionDiagnosticCLI(t *testing.T) {
	input, _, _ := cliFixture(t, 1)
	backup := encodeBackupDirectory(t, input, file.FormatBin, true)
	encoded := t.TempDir()
	copyBackupCollection(t, backup, encoded, "2A3", true)
	// A valid archive named as an AppleDouble sidecar is still not a collection.
	sidecar, err := os.ReadFile(filepath.Join(backup, "2B3.tar"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(encoded, "._2B3.tar"), sidecar, 0600); err != nil {
		t.Fatal(err)
	}
	for _, dry := range []bool{false, true} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("dry=%t/existing=%t", dry, existing), func(t *testing.T) {
				output := filepath.Join(t.TempDir(), "restore")
				if existing {
					if err := os.Mkdir(output, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(output, "keep"), []byte("keep me"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				args := []string{"decode", encoded, output, "-clear"}
				if dry {
					args = append(args, "-dryrun")
				}
				log, err := runCLI(t, args...)
				if err == nil || !strings.Contains(log, "found 1 collection, at least 2 required to decode") || strings.Contains(log, "totalCopies") || strings.Contains(log, "Ignored") {
					t.Fatalf("unexpected insufficient-collection diagnostic: %v\n%s", err, log)
				}
				if existing {
					if got, err := os.ReadFile(filepath.Join(output, "keep")); err != nil || string(got) != "keep me" {
						t.Fatalf("destination cleared: %q, %v", got, err)
					}
				} else if _, err := os.Stat(output); !os.IsNotExist(err) {
					t.Fatalf("destination created: %v", err)
				}
			})
		}
	}
}

func TestDecodeIgnoredChunkNamesDiagnosticCLI(t *testing.T) {
	input, _, want := cliFixture(t, 1)
	backup := encodeBackupDirectory(t, input, file.FormatPNG, false)
	for _, layout := range []string{"loose", "tar", "renamed-tar"} {
		for _, recognized := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/recognized=%t", layout, recognized), func(t *testing.T) {
				encoded := t.TempDir()
				for _, name := range []string{"2A3", "2B3"} {
					copyBackupCollection(t, backup, encoded, name, false)
					dir := filepath.Join(encoded, name)
					entries, err := os.ReadDir(dir)
					if err != nil {
						t.Fatal(err)
					}
					for _, entry := range entries {
						if !recognized {
							if err := os.Rename(filepath.Join(dir, entry.Name()), filepath.Join(dir, "copy-"+entry.Name())); err != nil {
								t.Fatal(err)
							}
						}
					}
					for _, extra := range []string{"photo.png", "notes.bin", "._IMG2A3_0001.PNG", "notes.txt"} {
						if err := os.WriteFile(filepath.Join(dir, extra), []byte("ignored"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					if layout != "loose" {
						archive, err := file.TarCollection(context.Background(), dir)
						if err != nil {
							t.Fatal(err)
						}
						if layout == "renamed-tar" {
							if err := os.Rename(archive, filepath.Join(encoded, "copy-"+name+".tar")); err != nil {
								t.Fatal(err)
							}
						}
						if err := os.RemoveAll(dir); err != nil {
							t.Fatal(err)
						}
					}
				}
				output := filepath.Join(t.TempDir(), "restore")
				log, err := runCLI(t, "decode", encoded, output)
				count := 3
				if recognized {
					count = 2
				}
				if !strings.Contains(log, fmt.Sprintf("%d BIN/PNG files", count)) || !strings.Contains(log, "for example") || strings.Contains(log, "for example \"._") {
					t.Fatalf("missing normal-level ignored-filename summary:\n%s", log)
				}
				if recognized {
					if err != nil {
						t.Fatalf("restore failed: %v\n%s", err, log)
					}
					if got, err := os.ReadFile(filepath.Join(output, "source.bin")); err != nil || !bytes.Equal(got, want) {
						t.Fatalf("restore changed: %v", err)
					}
				} else {
					if err == nil {
						t.Fatal("accepted arbitrary prefixed names")
					}
					if _, err := os.Stat(output); !os.IsNotExist(err) {
						t.Fatalf("destination created: %v", err)
					}
				}
			})
		}
	}
}
