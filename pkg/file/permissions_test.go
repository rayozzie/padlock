// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func assertOutputPermissions(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return // Windows uses ACLs rather than Unix permission bits.
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s permissions = %04o, want %04o", path, got, want)
	}
}

func TestCollectionDirectoryPermissions(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	out := filepath.Join(base, "new", "output")
	if err := PrepareOutputDirectory(ctx, out, false); err != nil {
		t.Fatal(err)
	}
	assertOutputPermissions(t, filepath.Dir(out), 0700)
	assertOutputPermissions(t, out, 0700)
	coll, err := CreateCollectionDirectory(ctx, out, "2A2")
	if err != nil {
		t.Fatal(err)
	}
	assertOutputPermissions(t, coll, 0700)

	// An existing user-selected directory keeps its permissions.
	existing := filepath.Join(base, "existing")
	if err := os.Mkdir(existing, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(existing, 0755); err != nil {
		t.Fatal(err)
	}
	if err := PrepareOutputDirectory(ctx, existing, false); err != nil {
		t.Fatal(err)
	}
	assertOutputPermissions(t, existing, 0755)
}

func TestCollectionChunkPermissions(t *testing.T) {
	ctx := context.Background()
	for _, format := range []Format{FormatBin, FormatPNG} {
		for _, named := range []bool{false, true} {
			name := string(format)
			if named {
				name += "_named"
			}
			t.Run(name, func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "new", "2A2")
				formatter := GetFormatter(format)
				write := func() error {
					if named {
						return WriteNamedChunk(ctx, formatter, dir, "2A2", 1, []byte("private share"))
					}
					return formatter.WriteChunk(ctx, dir, 0, 1, []byte("private share"))
				}
				if err := write(); err != nil {
					t.Fatal(err)
				}
				assertOutputPermissions(t, filepath.Dir(dir), 0700)
				assertOutputPermissions(t, dir, 0700)
				entries, err := os.ReadDir(dir)
				if err != nil || len(entries) != 1 {
					t.Fatalf("expected one chunk: entries=%v, err=%v", entries, err)
				}
				path := filepath.Join(dir, entries[0].Name())
				assertOutputPermissions(t, path, 0600)

				// Reusing a public file must fail before any new share data is written.
				if err := os.WriteFile(path, []byte("existing public file"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
				if err := write(); !errors.Is(err, os.ErrExist) {
					t.Fatalf("expected existing-file error, got %v", err)
				}
				data, err := os.ReadFile(path)
				if err != nil || string(data) != "existing public file" {
					t.Fatalf("existing file changed: %q, err=%v", data, err)
				}
			})
		}
	}
}

func assertPrivateTarEntries(t *testing.T, path string, want int) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	count := 0
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Mode != 0600 {
			t.Errorf("tar entry %q permissions = %04o, want 0600", header.Name, header.Mode)
		}
		count++
	}
	if count != want {
		t.Errorf("tar contains %d entries, want %d", count, want)
	}
}

func TestStreamingTarPermissions(t *testing.T) {
	ctx := context.Background()
	for _, format := range []Format{FormatBin, FormatPNG} {
		t.Run(string(format), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "new", "2A2.tar")
			writer, err := NewTarChunkWriter(ctx, path, "2A2", format)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := FinalizeAllTarWriters(ctx); err != nil {
					t.Error(err)
				}
			})
			// Check permissions while the archive is open, before writing secrets.
			assertOutputPermissions(t, filepath.Dir(path), 0700)
			assertOutputPermissions(t, path, 0600)
			for chunk := 1; chunk <= 2; chunk++ {
				next, err := NewTarChunkWriter(ctx, path, "2A2", format)
				if err != nil {
					t.Fatal(err)
				}
				if next != writer {
					t.Fatal("subsequent chunks must reuse the open writer")
				}
				next.ChunkNum = chunk
				if _, err := next.Write([]byte("private share")); err != nil {
					t.Fatal(err)
				}
				if err := next.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.FinalizeTar(); err != nil {
				t.Fatal(err)
			}
			assertPrivateTarEntries(t, path, 2)

			if err := os.Chmod(path, 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := NewTarChunkWriter(ctx, path, "2A2", format); !errors.Is(err, os.ErrExist) {
				t.Fatalf("expected existing-file error, got %v", err)
			}
			assertPrivateTarEntries(t, path, 2)
		})
	}
}

func TestCollectionArchivePermissions(t *testing.T) {
	ctx := context.Background()
	for _, contentsOnly := range []bool{false, true} {
		name := "collection"
		if contentsOnly {
			name = "contents"
		}
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "2A2")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			chunk := filepath.Join(dir, "2A2_0001.bin")
			if err := os.WriteFile(chunk, []byte("older public share"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(chunk, 0644); err != nil {
				t.Fatal(err)
			}
			archive := func() (string, error) {
				if contentsOnly {
					return TarDirectoryContents(ctx, dir, "2A2")
				}
				return TarCollection(ctx, dir)
			}
			path, err := archive()
			if err != nil {
				t.Fatal(err)
			}
			assertOutputPermissions(t, path, 0600)
			assertPrivateTarEntries(t, path, 1)
			if err := os.Chmod(path, 0644); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := archive(); !errors.Is(err, os.ErrExist) {
				t.Fatalf("expected existing-file error, got %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatalf("existing archive changed: err=%v", err)
			}
		})
	}
}
