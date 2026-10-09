// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type extractedDirectory struct {
	name string
	mode os.FileMode
	info os.FileInfo
}

// tarExtractor only creates directories and regular files beneath root. New
// directories stay accessible until all entries have been processed. Existing
// directories are never added to the permission restoration list.
type tarExtractor struct {
	root        *os.Root
	directories map[string]*extractedDirectory
	created     []*extractedDirectory // Ancestors are always created before children.
}

func newTarExtractor(root *os.Root) *tarExtractor {
	return &tarExtractor{root: root, directories: make(map[string]*extractedDirectory)}
}

// validateTarEntryHeader defines the entry set accepted by both extraction and
// dry-run validation. Filesystem-specific conflicts are checked during extraction.
func validateTarEntryHeader(header *tar.Header) (string, error) {
	if err := RejectSparseTarEntry(header); err != nil {
		return "", err
	}
	name := filepath.FromSlash(header.Name)
	if !filepath.IsLocal(name) {
		return "", fmt.Errorf("unsafe path in tar archive: %q", header.Name)
	}
	switch header.Typeflag {
	case tar.TypeDir, tar.TypeReg, tar.TypeRegA:
		return filepath.Clean(name), nil
	default:
		return "", fmt.Errorf("unsupported tar entry type %q for %q", header.Typeflag, header.Name)
	}
}

func (e *tarExtractor) extract(header *tar.Header, contents io.Reader) (int64, error) {
	name, err := validateTarEntryHeader(header)
	if err != nil {
		return 0, err
	}
	if header.Typeflag == tar.TypeDir {
		err := e.mkdirAll(name)
		if dir := e.directories[name]; dir != nil {
			// Earlier files may have created this directory implicitly. The
			// last explicit directory header supplies the saved mode.
			dir.mode = os.FileMode(header.Mode).Perm()
		}
		return 0, err
	}

	if err := e.mkdirAll(filepath.Dir(name)); err != nil {
		return 0, fmt.Errorf("create parent directory for %q: %w", header.Name, err)
	}

	// Exclusive creation also prevents overwriting an existing hard link to a
	// file outside root. Duplicate file entries are rejected for the same reason.
	out, err := e.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(header.Mode).Perm())
	if err != nil {
		return 0, fmt.Errorf("create extracted file %q: %w", header.Name, err)
	}
	n, copyErr := io.Copy(out, contents)
	closeErr := out.Close()
	if copyErr != nil {
		return n, fmt.Errorf("write extracted file %q: %w", header.Name, copyErr)
	}
	if closeErr != nil {
		return n, fmt.Errorf("close extracted file %q: %w", header.Name, closeErr)
	}
	return n, nil
}

func (e *tarExtractor) mkdirAll(name string) error {
	var nameSoFar string
	for _, part := range strings.Split(filepath.Clean(name), string(filepath.Separator)) {
		if part == "." {
			continue
		}
		nameSoFar = filepath.Join(nameSoFar, part)
		if err := e.root.Mkdir(nameSoFar, 0755); err != nil {
			info, statErr := e.root.Stat(nameSoFar)
			if statErr != nil || !info.IsDir() {
				return fmt.Errorf("create extraction directory %q: %w", nameSoFar, err)
			}
			continue
		}
		info, err := e.root.Lstat(nameSoFar)
		if err != nil {
			return fmt.Errorf("stat new extraction directory %q: %w", nameSoFar, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("new extraction directory %q was replaced", nameSoFar)
		}
		// Preserve the umask-adjusted default for implicit parents. Explicit
		// directory headers replace it with their saved permission bits.
		dir := &extractedDirectory{name: nameSoFar, mode: info.Mode().Perm(), info: info}
		e.directories[nameSoFar] = dir
		e.created = append(e.created, dir)
		if err := e.setDirectoryMode(dir, 0700); err != nil {
			return fmt.Errorf("prepare extraction directory %q: %w", nameSoFar, err)
		}
	}
	return nil
}

// setDirectoryMode uses a confined directory handle and verifies its identity
// before chmod. It never applies permissions through an unchecked filesystem
// path, and only holds handles for one directory at a time (also on Go 1.24).
func (e *tarExtractor) setDirectoryMode(dir *extractedDirectory, mode os.FileMode) (err error) {
	if runtime.GOOS == "windows" {
		// Windows directory access uses ACLs, and Mkdir ignores Unix modes.
		// Keep that behavior; File.Chmod would change the DOS read-only
		// attribute and can fail on a directory handle opened for reading.
		return nil
	}
	root, err := e.root.OpenRoot(dir.name)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(dir.info, info) {
		return fmt.Errorf("extraction directory %q was replaced", dir.name)
	}
	return f.Chmod(mode)
}

// finish runs even after extraction fails, so partially restored directories
// do not retain temporary permissions. Children must be finalized first, since
// an archived parent mode may remove the access needed to reach them.
func (e *tarExtractor) finish() error {
	var errs []error
	for i := len(e.created) - 1; i >= 0; i-- {
		dir := e.created[i]
		if err := e.setDirectoryMode(dir, dir.mode); err != nil {
			errs = append(errs, fmt.Errorf("restore permissions for directory %q: %w", dir.name, err))
		}
	}
	return errors.Join(errs...)
}

// mkdirAllInRoot uses the Root operations available in Go 1.24; Root.MkdirAll
// was added later. Every lookup and creation remains confined to the same root.
func mkdirAllInRoot(root *os.Root, name string, perm os.FileMode) error {
	var dir string
	for _, part := range strings.Split(filepath.Clean(name), string(filepath.Separator)) {
		if part == "." {
			continue
		}
		dir = filepath.Join(dir, part)
		if err := root.Mkdir(dir, perm); err != nil {
			info, statErr := root.Stat(dir)
			if statErr != nil || !info.IsDir() {
				return fmt.Errorf("create extraction directory %q: %w", dir, err)
			}
		}
	}
	return nil
}
