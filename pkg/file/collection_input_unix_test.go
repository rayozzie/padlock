// Copyright 2025 Ray Ozzie. All rights reserved.

//go:build unix

package file

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCollectionFileRejection(t *testing.T) {
	if root := os.Getenv("PADLOCK_COLLECTION_INPUT_TEST_ROOT"); root != "" {
		ctx := context.Background()
		check := func(t *testing.T, path string, err error) {
			t.Helper()
			if !errors.Is(err, ErrInvalidCollectionFile) || !strings.Contains(err.Error(), path) {
				t.Fatalf("expected invalid-collection error identifying %q, got %v", path, err)
			}
		}
		for _, kind := range []string{"fifo", "directory", "symlink", "regular"} {
			t.Run("replacement/"+kind, func(t *testing.T) {
				path := filepath.Join(root, "replaced-"+kind)
				if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
				expected, err := os.Lstat(path)
				if err != nil {
					t.Fatal(err)
				}
				// Keep the old inode allocated so a replacement cannot reuse it.
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "fifo":
					err = syscall.Mkfifo(path, 0600)
				case "directory":
					err = os.Mkdir(path, 0700)
				case "symlink":
					err = os.Symlink(path+".old", path)
				case "regular":
					err = os.WriteFile(path, []byte("replacement"), 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
				f, err := openCheckedCollectionFile(path, expected)
				if f != nil {
					f.Close()
					t.Fatal("opened a substituted collection file")
				}
				check(t, path, err)
			})
		}
		t.Run("archive-apis", func(t *testing.T) {
			path := filepath.Join(root, "stray.tar")
			if err := syscall.Mkfifo(path, 0600); err != nil {
				t.Fatal(err)
			}
			_, err := findTarCollection(ctx, path)
			check(t, path, err)
			_, err = ExtractTarCollection(ctx, path, filepath.Join(root, "extract"))
			check(t, path, err)
			reader := NewCollectionReader(Collection{Name: "2A2", Path: path, Format: FormatBin})
			defer reader.Close()
			_, firstErr := reader.ReadNextChunk(ctx)
			check(t, path, firstErr)
			_, err = reader.ReadNextChunk(ctx)
			if err != firstErr {
				t.Fatalf("TAR failure was not terminal: %v, then %v", firstErr, err)
			}
			if _, err := os.Stat(filepath.Join(root, "extract")); !os.IsNotExist(err) {
				t.Fatalf("special archive created extraction output: %v", err)
			}
		})
		for _, format := range []Format{FormatBin, FormatPNG} {
			t.Run("loose/"+string(format), func(t *testing.T) {
				dir := filepath.Join(root, string(format), "2A2")
				formatter := GetFormatter(format)
				for n := 1; n <= 2; n++ {
					if err := WriteNamedChunk(ctx, formatter, dir, "2A2", n, []byte("chunk")); err != nil {
						t.Fatal(err)
					}
				}
				entries, err := os.ReadDir(dir)
				if err != nil || len(entries) != 2 {
					t.Fatalf("fixture: %v, %v", entries, err)
				}
				reader := NewCollectionReader(Collection{Name: "2A2", Path: dir, Format: format})
				defer reader.Close()
				if _, err := reader.ReadNextChunk(ctx); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, entries[1].Name())
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
				// Initialization already saw an ordinary file. Opening the next
				// chunk must check again instead of trusting the cached list.
				_, err = reader.ReadNextChunk(ctx)
				check(t, path, err)
				_, err = formatter.ReadChunk(ctx, dir, 0, 2)
				check(t, path, err)
				fresh := NewCollectionReader(Collection{Name: "2A2", Path: dir, Format: format})
				defer fresh.Close()
				_, err = fresh.ReadNextChunk(ctx)
				check(t, path, err) // Reject chunk 2 before returning chunk 1.
			})
		}
		f, err := openCollectionFile("/dev/null")
		if f != nil {
			f.Close()
			t.Fatal("opened a device as a collection")
		}
		check(t, "/dev/null", err)
		return
	}
	// All potentially blocking operations run in a killable subprocess.
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestCollectionFileRejection$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), "PADLOCK_COLLECTION_INPUT_TEST_ROOT="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil || ctx.Err() != nil {
		t.Fatalf("collection rejection failed or hung: %v, %v\n%s", err, ctx.Err(), out)
	}
}

func TestCollectionFileAllowsDirectoryAliasesAndHardlinks(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "original")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "2A2_0001.bin")
	if err := os.WriteFile(path, []byte("chunk"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	hardlink := filepath.Join(root, "hardlink.bin")
	if err := os.Link(path, hardlink); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{path, filepath.Join(alias, filepath.Base(path)), hardlink} {
		if data, err := readCollectionFile(input, FormatBin); err != nil || string(data) != "chunk" {
			t.Fatalf("ordinary alias rejected: %q, %v", data, err)
		}
	}
	_, err := openCollectionFile(filepath.Join(root, "missing"))
	if !errors.Is(err, os.ErrNotExist) || !errors.Is(err, ErrInvalidCollectionFile) {
		t.Fatalf("open failure lost underlying cause: %v", err)
	}
}
