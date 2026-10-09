// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func readSerializedInput(t *testing.T, input string) []byte {
	t.Helper()
	stream, err := SerializeDirectoryToStream(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(stream)
	if err := errors.Join(readErr, stream.Close()); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSerializationResolvesRootSymlinks(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	input := filepath.Join(root, "input")
	if err := os.MkdirAll(filepath.Join(input, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("directory contents behind a root alias\n"), 2048)
	if err := os.WriteFile(filepath.Join(input, "nested", "source.txt"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	for _, link := range []struct{ name, target string }{
		{"absolute", input},
		{"relative", "input"},
		{"chained", "relative"},
		{"parent", root},
		{"leaf", filepath.Join(input, "nested")},
		{filepath.Join("input", "skipped-file"), "nested/source.txt"},
		{filepath.Join("input", "skipped-directory"), root},
		{filepath.Join("input", "skipped-dangling"), "missing"},
	} {
		if err := os.Symlink(link.target, link.name); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	want := readSerializedInput(t, input)
	// Establish that the reference contains the file and no internal links.
	tr := tar.NewReader(bytes.NewReader(want))
	for _, name := range []string{"nested", filepath.Join("nested", "source.txt")} {
		header, err := tr.Next()
		if err != nil || header.Name != name {
			t.Fatalf("wanted entry %q, got %+v, %v", name, header, err)
		}
		if header.Typeflag == tar.TypeReg {
			data, err := io.ReadAll(tr)
			if err != nil || !bytes.Equal(data, payload) {
				t.Fatalf("wrong serialized file contents: %v", err)
			}
		}
	}
	if header, err := tr.Next(); err != io.EOF {
		t.Fatalf("internal symlink entered the archive: %+v, %v", header, err)
	}
	for _, path := range []string{
		filepath.Join(root, "absolute"), "relative", "chained",
		filepath.Join("parent", "input"), "absolute" + string(os.PathSeparator),
		// Do not Join/Clean this spelling: the filesystem follows the link
		// before applying '..', so it selects input, not the test root.
		"leaf" + string(os.PathSeparator) + "..",
	} {
		t.Run(path, func(t *testing.T) {
			if got := readSerializedInput(t, path); !bytes.Equal(got, want) {
				t.Fatalf("aliased input produced a different TAR: got %d bytes, want %d identical bytes", len(got), len(want))
			}
		})
	}
}

func TestSerializationRejectsInvalidRootSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "regular"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, link := range []struct{ name, target string }{
		{"dangling", "missing"}, {"file-alias", "regular"}, {"loop-a", "loop-b"}, {"loop-b", "loop-a"},
	} {
		if err := os.Symlink(link.target, filepath.Join(root, link.name)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	for _, name := range []string{"dangling", "file-alias", "loop-a"} {
		t.Run(name, func(t *testing.T) {
			stream, err := SerializeDirectoryToStream(context.Background(), filepath.Join(root, name))
			if stream != nil {
				_ = stream.Close()
			}
			if err == nil || stream != nil {
				t.Fatalf("invalid root returned a stream or no error: stream=%v, err=%v", stream, err)
			}
		})
	}
}
