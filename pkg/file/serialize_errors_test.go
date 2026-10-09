// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestSerializationReportsTruncatedSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.txt")
	data := bytes.Repeat([]byte("x"), 1024*1024)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	stream, err := SerializeDirectoryToStream(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	// Read the advertised size, then stop consuming the pipe while truncating
	// the source. The producer cannot finish sending the large file beforehand.
	header, err := tar.NewReader(stream).Next()
	if err != nil {
		t.Fatal(err)
	}
	if header.Size != int64(len(data)) {
		t.Fatalf("source size = %d, want %d", header.Size, len(data))
	}
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, stream); err == nil {
		t.Fatal("serialization reported a normal EOF despite a truncated source")
	}
}
