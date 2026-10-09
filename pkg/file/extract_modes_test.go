// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func directoryModeTar(t *testing.T, entries ...extractionTestEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, entry := range entries {
		header := entry.header
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

func unlockDirectoriesAfterTest(t *testing.T, root string, names ...string) {
	t.Helper()
	t.Cleanup(func() {
		// Callers list parents first so non-searchable directories can be removed.
		for _, name := range names {
			_ = os.Chmod(filepath.Join(root, name), 0700)
		}
	})
}

func TestArchiveExtractionDefersDirectoryPermissions(t *testing.T) {
	// On Windows the mode assertions are skipped, but all archives must still
	// extract successfully using the destination's directory ACLs.
	for _, extractor := range archiveExtractors {
		for _, mode := range []int64{0000, 0400, 0500, 0555} {
			for _, lateHeaders := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/mode=%04o/late_headers=%t", extractor.name, mode, lateHeaders), func(t *testing.T) {
					dirs := []extractionTestEntry{
						{header: tar.Header{Name: "locked", Typeflag: tar.TypeDir, Mode: mode}},
						{header: tar.Header{Name: "locked/nested", Typeflag: tar.TypeDir, Mode: 0000}},
						{header: tar.Header{Name: "locked/nested/empty", Typeflag: tar.TypeDir, Mode: 0555}},
					}
					file := extractionTestEntry{
						header: tar.Header{Name: "locked/nested/data.txt", Typeflag: tar.TypeReg, Mode: 0600}, body: "nested contents",
					}
					entries := append(append([]extractionTestEntry(nil), dirs...), file)
					if lateHeaders {
						// File entries create implicit parents; later headers must
						// update their final modes without changing finalization order.
						entries = []extractionTestEntry{file, dirs[2], dirs[1], dirs[0]}
					}
					dest := filepath.Join(t.TempDir(), "restored")
					unlockDirectoriesAfterTest(t, dest, "locked", "locked/nested", "locked/nested/empty")
					if err := extractor.extract(t, dest, directoryModeTar(t, entries...)); err != nil {
						t.Fatal(err)
					}
					for _, dir := range dirs {
						path := filepath.Join(dest, dir.header.Name)
						assertOutputPermissions(t, path, os.FileMode(dir.header.Mode))
						// Inspect each final mode before allowing access to its children.
						if err := os.Chmod(path, 0700); err != nil {
							t.Fatal(err)
						}
					}
					assertExtractionFile(t, filepath.Join(dest, file.header.Name), file.body)
				})
			}
		}
	}
}

func TestArchiveExtractionPreservesExistingDirectoryModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits")
	}
	for _, extractor := range archiveExtractors {
		t.Run(extractor.name, func(t *testing.T) {
			base := t.TempDir()
			dest := filepath.Join(base, "restored")
			existing := filepath.Join(dest, "existing")
			if err := os.MkdirAll(existing, 0700); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{dest, existing} {
				if err := os.Chmod(path, 0750); err != nil {
					t.Fatal(err)
				}
			}
			// Implicit parents without an archive header keep the usual
			// default permissions, including the current process's umask.
			control := filepath.Join(base, "default-mode")
			if err := os.Mkdir(control, 0755); err != nil {
				t.Fatal(err)
			}
			defaultInfo, err := os.Stat(control)
			if err != nil {
				t.Fatal(err)
			}
			archive := directoryModeTar(t,
				extractionTestEntry{header: tar.Header{Name: ".", Typeflag: tar.TypeDir, Mode: 0000}},
				extractionTestEntry{header: tar.Header{Name: "existing", Typeflag: tar.TypeDir, Mode: 0555}},
				extractionTestEntry{header: tar.Header{Name: "existing/new", Typeflag: tar.TypeDir, Mode: 0500}},
				extractionTestEntry{header: tar.Header{Name: "existing/new/file.txt", Typeflag: tar.TypeReg, Mode: 0600}, body: "restored"},
				extractionTestEntry{header: tar.Header{Name: "implicit/nested/file.txt", Typeflag: tar.TypeReg, Mode: 0600}, body: "implicit parents"},
			)
			unlockDirectoriesAfterTest(t, dest, "existing/new")
			if err := extractor.extract(t, dest, archive); err != nil {
				t.Fatal(err)
			}
			assertOutputPermissions(t, dest, 0750)
			assertOutputPermissions(t, existing, 0750)
			assertOutputPermissions(t, filepath.Join(existing, "new"), 0500)
			for _, name := range []string{"implicit", "implicit/nested"} {
				assertOutputPermissions(t, filepath.Join(dest, name), defaultInfo.Mode().Perm())
			}
			assertExtractionFile(t, filepath.Join(existing, "new/file.txt"), "restored")
			assertExtractionFile(t, filepath.Join(dest, "implicit/nested/file.txt"), "implicit parents")
		})
	}
}

func TestArchiveExtractionRestoresModesAfterFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits")
	}
	archive := directoryModeTar(t,
		extractionTestEntry{header: tar.Header{Name: "locked", Typeflag: tar.TypeDir, Mode: 0555}},
		extractionTestEntry{header: tar.Header{Name: "locked/nested", Typeflag: tar.TypeDir, Mode: 0000}},
		extractionTestEntry{header: tar.Header{Name: "locked/nested/data.txt", Typeflag: tar.TypeReg, Mode: 0600}, body: "incomplete contents"},
	)
	// Two directory headers and a file header, followed by only part of the file.
	archive = archive[:3*512+3]
	for _, extractor := range archiveExtractors {
		t.Run(extractor.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "restored")
			unlockDirectoriesAfterTest(t, dest, "locked", "locked/nested")
			err := extractor.extract(t, dest, archive)
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("got %v, want original archive read error", err)
			}
			assertOutputPermissions(t, filepath.Join(dest, "locked"), 0555)
			assertOutputPermissions(t, filepath.Join(dest, "locked/nested"), 0000)
		})
	}
}

type directoryMutationReader struct {
	io.Reader
	mutate func() error
}

func (r *directoryMutationReader) Read(p []byte) (int, error) {
	if r.mutate != nil {
		mutate := r.mutate
		r.mutate = nil
		if err := mutate(); err != nil {
			return 0, err
		}
	}
	return r.Reader.Read(p)
}

func TestRestoreDirectoryModesRejectsReplacedDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits and symlinks")
	}
	archive := directoryModeTar(t,
		extractionTestEntry{header: tar.Header{Name: "locked", Typeflag: tar.TypeDir, Mode: 0555}},
		extractionTestEntry{header: tar.Header{Name: "locked/data.txt", Typeflag: tar.TypeReg, Mode: 0600}, body: "contents"},
	)
	for _, replacement := range []string{"outside_symlink", "inside_symlink", "directory", "removed"} {
		for _, readFailure := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/read_failure=%t", replacement, readFailure), func(t *testing.T) {
				base := t.TempDir()
				dest, outside := filepath.Join(base, "restored"), filepath.Join(base, "outside")
				victim := filepath.Join(dest, "victim")
				for _, path := range []string{outside, victim} {
					if err := os.MkdirAll(path, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(path, 0750); err != nil {
						t.Fatal(err)
					}
				}
				failure := errors.New("archive read failed during finalization")
				var tail io.Reader = bytes.NewReader(archive[len(archive)-1024:])
				if readFailure {
					tail = &archiveEndingErrorReader{err: failure}
				}
				reader := io.MultiReader(bytes.NewReader(archive[:len(archive)-1024]), &directoryMutationReader{
					Reader: tail,
					mutate: func() error {
						path := filepath.Join(dest, "locked")
						if err := os.Rename(path, filepath.Join(dest, "moved")); err != nil {
							return err
						}
						switch replacement {
						case "outside_symlink":
							return os.Symlink(outside, path)
						case "inside_symlink":
							return os.Symlink("victim", path)
						case "directory":
							if err := os.Mkdir(path, 0700); err != nil {
								return err
							}
							return os.Chmod(path, 0750)
						}
						return nil
					},
				})
				err := DeserializeDirectoryFromStream(context.Background(), dest, reader, false)
				if err == nil || !strings.Contains(err.Error(), `restore permissions for directory "locked"`) {
					t.Fatalf("permission restoration failure was lost: %v", err)
				}
				if readFailure && !errors.Is(err, failure) {
					t.Errorf("original archive error was lost: %v", err)
				}
				assertOutputPermissions(t, outside, 0750)
				assertOutputPermissions(t, victim, 0750)
				if replacement == "directory" {
					assertOutputPermissions(t, filepath.Join(dest, "locked"), 0750)
				}
				assertExtractionFile(t, filepath.Join(dest, "moved/data.txt"), "contents")
			})
		}
	}
}
