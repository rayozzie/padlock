// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

type extractionTestEntry struct {
	header tar.Header
	body   string
}

func extractionTestTar(t *testing.T, entries ...extractionTestEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, entry := range entries {
		header := entry.header
		header.Mode = 0755
		header.Size = int64(len(entry.body))
		if err := w.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func extractionTestGzip(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Exercise both public extraction entry points, including compressed restores.
var archiveExtractors = []struct {
	name    string
	extract func(*testing.T, string, []byte) error
}{
	{"restore", func(t *testing.T, dest string, data []byte) error {
		return DeserializeDirectoryFromStream(context.Background(), dest, bytes.NewReader(data), false)
	}},
	{"restore_gzip", func(t *testing.T, dest string, data []byte) error {
		return DeserializeDirectoryFromStream(context.Background(), dest, bytes.NewReader(extractionTestGzip(t, data)), false)
	}},
	{"collection", func(t *testing.T, dest string, data []byte) error {
		t.Helper()
		archive := filepath.Join(t.TempDir(), filepath.Base(dest)+".tar")
		if err := os.WriteFile(archive, data, 0600); err != nil {
			t.Fatal(err)
		}
		_, err := ExtractTarCollection(context.Background(), archive, filepath.Dir(dest))
		return err
	}},
}

func assertExtractionFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Errorf("%s contains %q, want %q", path, data, want)
	}
}

func TestArchiveExtractionRejectsEscapingPaths(t *testing.T) {
	for _, extractor := range archiveExtractors {
		t.Run(extractor.name, func(t *testing.T) {
			for _, kind := range []byte{tar.TypeReg, tar.TypeDir} {
				for _, pathKind := range []string{"parent", "nested_parent", "sibling_prefix", "absolute"} {
					t.Run(string(kind)+"/"+pathKind, func(t *testing.T) {
						base := t.TempDir()
						dest := filepath.Join(base, "collection")
						outside := filepath.Join(base, "collection-other")
						if err := os.Mkdir(outside, 0755); err != nil {
							t.Fatal(err)
						}
						victim := filepath.Join(outside, "keep.txt")
						if err := os.WriteFile(victim, []byte("original"), 0600); err != nil {
							t.Fatal(err)
						}
						target := victim
						if kind == tar.TypeDir {
							target = filepath.Join(outside, "new-directory")
						}
						var name string
						switch pathKind {
						case "parent":
							name = "../" + filepath.Base(target)
							if kind == tar.TypeReg {
								victim = filepath.Join(base, "keep.txt")
								if err := os.WriteFile(victim, []byte("original"), 0600); err != nil {
									t.Fatal(err)
								}
							}
							target = filepath.Join(base, filepath.Base(target))
						case "nested_parent":
							name = "nested/../../collection-other/" + filepath.Base(target)
						case "sibling_prefix":
							name = "../collection-other/" + filepath.Base(target)
						case "absolute":
							name = filepath.ToSlash(target)
						}
						entry := extractionTestEntry{header: tar.Header{Name: name, Typeflag: kind}}
						if kind == tar.TypeReg {
							entry.body = "overwritten"
						}
						if err := extractor.extract(t, dest, extractionTestTar(t, entry)); err == nil {
							t.Errorf("accepted unsafe archive path %q", name)
						}
						assertExtractionFile(t, victim, "original")
						if kind == tar.TypeDir {
							if _, err := os.Lstat(target); !os.IsNotExist(err) {
								t.Errorf("created outside directory %s: %v", target, err)
							}
						}
					})
				}
			}
		})
	}
}

func TestArchiveExtractionRejectsExistingLinks(t *testing.T) {
	for _, extractor := range archiveExtractors {
		for _, kind := range []string{"symlink_file", "symlink_directory", "dangling_symlink", "hardlink"} {
			t.Run(extractor.name+"/"+kind, func(t *testing.T) {
				base := t.TempDir()
				dest := filepath.Join(base, "collection")
				outside := filepath.Join(base, "outside")
				for _, dir := range []string{dest, outside} {
					if err := os.Mkdir(dir, 0755); err != nil {
						t.Fatal(err)
					}
				}
				victim := filepath.Join(outside, "keep.txt")
				if err := os.WriteFile(victim, []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
				name := "link"
				var err error
				switch kind {
				case "symlink_file":
					err = os.Symlink(victim, filepath.Join(dest, name))
				case "symlink_directory":
					err = os.Symlink("../outside", filepath.Join(dest, name))
					name += "/new/entry.txt"
				case "dangling_symlink":
					err = os.Symlink(filepath.Join(outside, "created.txt"), filepath.Join(dest, name))
				case "hardlink":
					err = os.Link(victim, filepath.Join(dest, name))
				}
				if err != nil {
					t.Skipf("filesystem cannot create test link: %v", err)
				}
				data := extractionTestTar(t, extractionTestEntry{header: tar.Header{Name: name, Typeflag: tar.TypeReg}, body: "overwritten"})
				if err := extractor.extract(t, dest, data); err == nil {
					t.Error("accepted archive entry through an existing link")
				}
				assertExtractionFile(t, victim, "original")
				entries, err := os.ReadDir(outside)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 1 || entries[0].Name() != "keep.txt" {
					t.Errorf("created entries outside destination: %v", entries)
				}
			})
		}
	}
}

func TestArchiveExtractionRejectsLinkAndSpecialEntries(t *testing.T) {
	for _, extractor := range archiveExtractors {
		for _, kind := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeFifo, tar.TypeChar, tar.TypeBlock} {
			t.Run(extractor.name+"/"+string(kind), func(t *testing.T) {
				dest := filepath.Join(t.TempDir(), "collection")
				header := tar.Header{Name: "unsafe", Typeflag: kind}
				if kind == tar.TypeSymlink || kind == tar.TypeLink {
					header.Linkname = "../outside"
				}
				if err := extractor.extract(t, dest, extractionTestTar(t, extractionTestEntry{header: header})); err == nil {
					t.Errorf("accepted unsupported entry type %q", kind)
				}
				entries, err := os.ReadDir(dest)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 {
					t.Errorf("created files for unsupported entry: %v", entries)
				}
			})
		}
	}
}

func TestArchiveExtractionPreservesFiles(t *testing.T) {
	entries := []extractionTestEntry{
		{header: tar.Header{Name: "directory/", Typeflag: tar.TypeDir}},
		{header: tar.Header{Name: "directory/nested/data.txt", Typeflag: tar.TypeReg}, body: "nested contents\x00\xff"},
		{header: tar.Header{Name: "./root.txt", Typeflag: tar.TypeReg}, body: "root contents"},
		{header: tar.Header{Name: "empty.txt", Typeflag: tar.TypeReg}},
	}
	for _, extractor := range archiveExtractors {
		t.Run(extractor.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "collection")
			if err := extractor.extract(t, dest, extractionTestTar(t, entries...)); err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.header.Typeflag == tar.TypeReg {
					assertExtractionFile(t, filepath.Join(dest, entry.header.Name), entry.body)
				}
			}
		})
	}
}

func TestDeserializeRawRejectsExistingSymlinks(t *testing.T) {
	for _, name := range []string{"decoded_data.txt", "decoded_data.bin", "decoded_output.dat"} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			dest := filepath.Join(base, "output")
			if err := os.Mkdir(dest, 0755); err != nil {
				t.Fatal(err)
			}
			victim := filepath.Join(base, "keep.txt")
			if err := os.WriteFile(victim, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(victim, filepath.Join(dest, name)); err != nil {
				t.Skipf("filesystem cannot create symlink: %v", err)
			}
			data := []byte("raw contents")
			if name == "decoded_data.bin" {
				data = []byte{0, 1, 2}
			} else if name == "decoded_output.dat" {
				data = extractionTestGzip(t, data)
			}
			if err := DeserializeDirectoryFromStream(context.Background(), dest, bytes.NewReader(data), false); err == nil {
				t.Error("raw restore followed a symlink outside destination")
			}
			assertExtractionFile(t, victim, "original")
		})
	}
}

func TestArchiveExtractionRejectsWindowsPaths(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path semantics")
	}
	for _, extractor := range archiveExtractors {
		for _, name := range []string{`..\outside.txt`, `C:\outside.txt`, `C:outside.txt`, `\outside.txt`, `\\server\share\outside.txt`, "NUL", "file:stream"} {
			t.Run(extractor.name+"/"+name, func(t *testing.T) {
				dest := filepath.Join(t.TempDir(), "collection")
				data := extractionTestTar(t, extractionTestEntry{header: tar.Header{Name: name, Typeflag: tar.TypeReg}, body: "contents"})
				if err := extractor.extract(t, dest, data); err == nil {
					t.Errorf("accepted unsafe Windows path %q", name)
				}
			})
		}
	}
}
