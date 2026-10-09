// Copyright 2025 Ray Ozzie. All rights reserved.

//go:build unix

package file

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSourceFileRejection(t *testing.T) {
	if kind := os.Getenv("PADLOCK_SOURCE_TEST_KIND"); kind != "" {
		dir := filepath.Join(os.Getenv("PADLOCK_SOURCE_TEST_ROOT"), "input")
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(dir, "keep.txt")
		if err := os.WriteFile(marker, []byte("source contents"), 0600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "special")
		var err error
		if strings.HasPrefix(kind, "replaced_") {
			if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			var expected os.FileInfo
			expected, err = os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			// Retain the old inode so a quick unlink/recreate cannot reuse it.
			if err := os.Rename(path, path+".old"); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "replaced_fifo":
				err = syscall.Mkfifo(path, 0600)
			case "replaced_directory":
				err = os.Mkdir(path, 0700)
			case "replaced_symlink":
				err = os.Symlink(marker, path)
			case "replaced_regular":
				err = os.WriteFile(path, []byte("replacement"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			f, _, openErr := openRegularSource(path, expected)
			if f != nil {
				f.Close()
				t.Fatal("returned an open handle for a substituted input")
			}
			err = openErr
		} else if kind == "device" {
			path = "/dev/null"
			info, statErr := os.Lstat(path)
			if statErr != nil {
				t.Fatal(statErr)
			}
			f, _, openErr := openRegularSource(path, info)
			if f != nil {
				f.Close()
				t.Fatal("opened a device as a source file")
			}
			err = openErr
		} else {
			if err := syscall.Mkfifo(path, 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "preflight":
				err = ValidateInputTree(context.Background(), dir)
			case "preflight_alias":
				alias := filepath.Join(filepath.Dir(dir), "alias")
				if err := os.Symlink(dir, alias); err != nil {
					t.Fatal(err)
				}
				err = ValidateInputTree(context.Background(), alias)
			case "serialize":
				var stream io.ReadCloser
				stream, err = SerializeDirectoryToStream(context.Background(), dir)
				if err == nil {
					_, err = io.Copy(io.Discard, stream)
					stream.Close()
				}
			case "tar_collection":
				_, err = TarCollection(context.Background(), dir)
			case "tar_contents":
				_, err = TarDirectoryContents(context.Background(), dir, "2A2")
			}
		}
		if err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("expected source rejection identifying %q, got %v", path, err)
		}
		if kind != "replaced_regular" && kind != "replaced_symlink" && !errors.Is(err, ErrUnsupportedSourceType) {
			t.Fatalf("expected unsupported-source error, got %v", err)
		}
		if got, err := os.ReadFile(marker); err != nil || string(got) != "source contents" {
			t.Fatalf("rejection changed source data: %q, %v", got, err)
		}
		return
	}
	for _, kind := range []string{"preflight", "preflight_alias", "serialize", "tar_collection", "tar_contents", "device", "replaced_fifo", "replaced_directory", "replaced_symlink", "replaced_regular"} {
		t.Run(kind, func(t *testing.T) {
			// Contain any future FIFO-open regression in a killable subprocess.
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestSourceFileRejection$", "-test.count=1")
			cmd.Env = append(os.Environ(), "PADLOCK_SOURCE_TEST_KIND="+kind, "PADLOCK_SOURCE_TEST_ROOT="+t.TempDir())
			output, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("source operation hung: %v\n%s", ctx.Err(), output)
			}
			if err != nil {
				t.Fatalf("source rejection failed: %v\n%s", err, output)
			}
		})
	}
}

func TestSourcePreflightAndSerializationSkipSymlinks(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "input")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "regular"), []byte("saved"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(dir, "regular"), filepath.Join(dir, "hardlink")); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(base, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{fifo, "fifo-link"}, {base, "directory-link"}, {"missing", "dangling-link"}, {"regular", "file-link"}} {
		if err := os.Symlink(pair[0], filepath.Join(dir, pair[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateInputTree(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	stream, err := SerializeDirectoryToStream(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	reader := tar.NewReader(stream)
	for _, want := range []string{"hardlink", "regular"} {
		header, err := reader.Next()
		if err != nil || header.Name != want || header.Typeflag != tar.TypeReg {
			t.Fatalf("wanted regular %q, got %+v, %v", want, header, err)
		}
		data, err := io.ReadAll(reader)
		if err != nil || string(data) != "saved" {
			t.Fatalf("wrong source data: %q, %v", data, err)
		}
	}
	if header, err := reader.Next(); err != io.EOF {
		t.Fatalf("symlink was not skipped: %+v, %v", header, err)
	}
}
