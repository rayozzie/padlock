// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type restorePermissionReader struct {
	io.Reader
	check func()
}

func (r *restorePermissionReader) Read(p []byte) (int, error) {
	if r.check != nil {
		r.check()
		r.check = nil
	}
	return r.Reader.Read(p)
}

func TestDeserializeOutputDirectoryPermissions(t *testing.T) {
	archive := directoryModeTar(t,
		extractionTestEntry{header: tar.Header{Name: ".", Typeflag: tar.TypeDir, Mode: 0777}},
		extractionTestEntry{header: tar.Header{Name: "restored.txt", Mode: 0644}, body: "restored data"},
	)
	for _, compressed := range []bool{false, true} {
		data := archive
		if compressed {
			var buf bytes.Buffer
			w := gzip.NewWriter(&buf)
			if _, err := w.Write(data); err != nil {
				t.Fatal(err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			data = buf.Bytes()
		}
		for _, existing := range []bool{false, true} {
			for _, clearOutput := range []bool{false, true} {
				t.Run(fmt.Sprintf("gzip=%t/existing=%t/clear=%t", compressed, existing, clearOutput), func(t *testing.T) {
					base := t.TempDir()
					if err := os.Chmod(base, 0755); err != nil {
						t.Fatal(err)
					}
					output := filepath.Join(base, "new", "restore")
					want := os.FileMode(0700)
					if existing {
						if err := os.MkdirAll(output, 0700); err != nil {
							t.Fatal(err)
						}
						want = 0750
						if err := os.Chmod(output, want); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(output, "keep"), []byte("keep"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					check := func() {
						assertOutputPermissions(t, base, 0755)
						assertOutputPermissions(t, filepath.Dir(output), 0700)
						assertOutputPermissions(t, output, want)
					}
					// Verify privacy before any archive bytes have been consumed,
					// as well as after extraction and permission restoration.
					r := &restorePermissionReader{Reader: bytes.NewReader(data), check: check}
					if err := DeserializeDirectoryFromStream(context.Background(), output, r, clearOutput); err != nil {
						t.Fatal(err)
					}
					check()
					if got, err := os.ReadFile(filepath.Join(output, "restored.txt")); err != nil || string(got) != "restored data" {
						t.Fatalf("restore failed: %q, %v", got, err)
					}
					_, err := os.Stat(filepath.Join(output, "keep"))
					if existing && !clearOutput {
						if err != nil {
							t.Fatalf("existing file removed without clear: %v", err)
						}
					} else if !os.IsNotExist(err) {
						t.Fatalf("unexpected retained file: %v", err)
					}
				})
			}
		}
	}
}
