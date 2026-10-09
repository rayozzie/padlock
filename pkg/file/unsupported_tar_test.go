// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectoryArchiveRejectsUnsupportedTypes(t *testing.T) {
	for _, kind := range []byte{tar.TypeFifo, tar.TypeChar, tar.TypeBlock, tar.TypeSymlink, tar.TypeLink, 'Z'} {
		header := tar.Header{Name: "unsupported", Typeflag: kind}
		if kind == tar.TypeSymlink || kind == tar.TypeLink {
			header.Linkname = "target.txt"
		}
		archive := extractionTestTar(t,
			extractionTestEntry{header: header},
			extractionTestEntry{header: tar.Header{Name: "after.txt", Typeflag: tar.TypeReg}, body: "must not mask failure"},
		)
		for _, compressed := range []bool{false, true} {
			data := archive
			if compressed {
				data = extractionTestGzip(t, data)
			}
			for _, check := range directoryArchiveChecks {
				t.Run(fmt.Sprintf("%c/gzip=%t/%s", kind, compressed, check.name), func(t *testing.T) {
					dest := filepath.Join(t.TempDir(), "restored")
					err := check.run(context.Background(), dest, bytes.NewReader(data))
					if err == nil || !strings.Contains(err.Error(), "unsupported tar entry type") {
						t.Fatalf("unsupported archive accepted or wrong error: %v", err)
					}
					for _, name := range []string{"unsupported", "after.txt"} {
						if _, err := os.Lstat(filepath.Join(dest, name)); !os.IsNotExist(err) {
							t.Errorf("created %q after unsupported entry: %v", name, err)
						}
					}
				})
			}
		}
	}
}
