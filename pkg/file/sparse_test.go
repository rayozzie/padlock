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
	"runtime"
	"testing"

	"github.com/rayozzie/padlock/internal/testarchive"
)

var sparseVersions = []string{"gnu", "pax0.0", "pax0.1", "pax1.0"}

// A small logical size keeps regressions safe: removing the rejection must
// fail these tests without exhausting the machine or filling its disk.
const sparseTestSize = 1 << 20

func TestSparseTARFixtures(t *testing.T) {
	for _, version := range sparseVersions {
		t.Run(version, func(t *testing.T) {
			data := testarchive.Sparse("entry.bin", 1<<40, version)
			if len(data) > 4096 {
				t.Fatalf("fixture unexpectedly large: %d", len(data))
			}
			reader := tar.NewReader(bytes.NewReader(data))
			header, err := reader.Next()
			if err != nil || header.Name != "entry.bin" || header.Size != 1<<40 {
				t.Fatalf("fixture was not parsed as a 1 TiB sparse file: header=%+v, err=%v", header, err)
			}
			if version != "gnu" && header.Typeflag != tar.TypeReg {
				t.Fatalf("PAX sparse fixture should look like a regular file, got %q", header.Typeflag)
			}
			prefix := make([]byte, 512)
			if _, err := io.ReadFull(reader, prefix); err != nil || !bytes.Equal(prefix, make([]byte, len(prefix))) {
				t.Fatalf("fixture did not synthesize hole bytes: %v", err)
			}
			if err := RejectSparseTarEntry(header); !errors.Is(err, ErrSparseTarEntry) {
				t.Fatalf("large sparse entry accepted: %v", err)
			}
		})
	}
}

func TestSparseTARDirectoryStreams(t *testing.T) {
	for _, version := range sparseVersions {
		archive := testarchive.Sparse("hole.bin", sparseTestSize, version)
		for _, compressed := range []bool{false, true} {
			data := archive
			name := version + "/tar"
			if compressed {
				data = extractionTestGzip(t, data)
				name = version + "/gzip"
			}
			for _, check := range directoryArchiveChecks {
				t.Run(name+"/"+check.name, func(t *testing.T) {
					dest := filepath.Join(t.TempDir(), "restored")
					err := check.run(context.Background(), dest, bytes.NewReader(data))
					if !errors.Is(err, ErrSparseTarEntry) {
						t.Fatalf("sparse entry accepted or wrong error: %v", err)
					}
					if _, err := os.Lstat(filepath.Join(dest, "hole.bin")); !os.IsNotExist(err) {
						t.Fatalf("created output for sparse entry: %v", err)
					}
				})
			}
		}
	}
}

func TestSparseTARCollectionRead(t *testing.T) {
	for _, version := range sparseVersions {
		for _, name := range []string{"2A2_0001.bin", "IMG2A2_0001.PNG", "ignored.txt"} {
			t.Run(version+"/"+name, func(t *testing.T) {
				archive := filepath.Join(t.TempDir(), "2A2.tar")
				if err := os.WriteFile(archive, testarchive.Sparse(name, sparseTestSize, version), 0600); err != nil {
					t.Fatal(err)
				}
				reader := NewCollectionReader(Collection{Name: "2A2", Path: archive})
				defer reader.Close()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				data, err := reader.ReadNextChunk(context.Background())
				runtime.ReadMemStats(&after)
				if !errors.Is(err, ErrSparseTarEntry) || len(data) != 0 {
					t.Fatalf("read sparse entry: bytes=%d, err=%v", len(data), err)
				}
				if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
					t.Errorf("allocated %d bytes before rejecting sparse entry", allocated)
				}
				if reader.tarFile != nil {
					t.Error("sparse-entry failure left the archive open")
				}
			})
		}
	}
}

func TestSparseTARCollectionDiscovery(t *testing.T) {
	for _, version := range sparseVersions {
		for _, archiveName := range []string{"2A2.tar", "renamed.tar"} {
			for _, entryName := range []string{"2A2_0001.bin", "IMG2A2_0001.PNG", "ignored.txt"} {
				t.Run(version+"/"+archiveName+"/"+entryName, func(t *testing.T) {
					base := t.TempDir()
					if err := os.WriteFile(filepath.Join(base, archiveName), testarchive.Sparse(entryName, sparseTestSize, version), 0600); err != nil {
						t.Fatal(err)
					}
					collections, temp, err := FindCollections(context.Background(), base)
					if temp != "" {
						defer os.RemoveAll(temp)
					}
					if !errors.Is(err, ErrSparseTarEntry) || len(collections) != 0 {
						t.Fatalf("discovered sparse collection: collections=%v, err=%v", collections, err)
					}
				})
			}
		}
	}
}

func TestSparseTARCollectionExtraction(t *testing.T) {
	for _, version := range sparseVersions {
		t.Run(version, func(t *testing.T) {
			base := t.TempDir()
			archive := filepath.Join(base, "2A2.tar")
			if err := os.WriteFile(archive, testarchive.Sparse("hole.bin", sparseTestSize, version), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := ExtractTarCollection(context.Background(), archive, filepath.Join(base, "extracted"))
			if !errors.Is(err, ErrSparseTarEntry) {
				t.Fatalf("sparse collection extracted or wrong error: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(base, "extracted", "2A2", "hole.bin")); !os.IsNotExist(err) {
				t.Fatalf("created output for sparse entry: %v", err)
			}
		})
	}
}

func TestSparseTARDiscoveryCleansTemporaryDirectory(t *testing.T) {
	input, scratch := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(input, "renamed.tar"), testarchive.Sparse("2A2_0001.bin", sparseTestSize, "pax1.0"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, scratch)
	}
	_, _, err := FindCollections(context.Background(), input)
	if !errors.Is(err, ErrSparseTarEntry) {
		t.Fatalf("sparse archive accepted or wrong error: %v", err)
	}
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed discovery leaked temporary extraction files: entries=%v, err=%v", entries, err)
	}
}

func TestNonSparsePAXDirectoryStream(t *testing.T) {
	var archive bytes.Buffer
	w := tar.NewWriter(&archive)
	header := &tar.Header{Name: "data.txt", Mode: 0600, Size: 4, Format: tar.FormatPAX,
		PAXRecords: map[string]string{"example.metadata": "preserved"}}
	if err := w.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for _, check := range directoryArchiveChecks {
		t.Run(check.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "restored")
			if err := check.run(context.Background(), dest, bytes.NewReader(archive.Bytes())); err != nil {
				t.Fatalf("ordinary PAX archive rejected: %v", err)
			}
			if check.name == "restore" {
				assertExtractionFile(t, filepath.Join(dest, "data.txt"), "data")
			}
		})
	}
}
