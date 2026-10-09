// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrUnsupportedSourceType = errors.New("unsupported source file type")

func unsupportedSourceType(path string, mode os.FileMode) error {
	return fmt.Errorf("%w: %q (%s); only regular files and directories can be backed up", ErrUnsupportedSourceType, path, mode.Type())
}

// ValidateInputTree checks supported file types and readability before the caller
// creates or clears output. Symlinks within the tree are skipped, as in the
// serializer. This is a preflight, not a snapshot: serialization checks again.
func ValidateInputTree(ctx context.Context, inputDir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Walk does not follow a symlink at its root. Resolve that root so even a
	// directory supplied through an alias receives the complete preflight.
	root, err := filepath.EvalSymlinks(inputDir)
	if err != nil {
		return fmt.Errorf("resolve input directory %q: %w", inputDir, err)
	}
	return filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return fmt.Errorf("inspect input %q: %w", path, walkErr)
		}
		if path == root && !info.IsDir() {
			return fmt.Errorf("input path is not a directory: %q", inputDir)
		}
		if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		f, _, err := openRegularSource(path, info)
		if err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close input %q after preflight: %w", path, err)
		}
		return nil
	})
}

// openRegularSource checks the opened object, not just the earlier walk result.
// It returns metadata from that same handle for the TAR header. On Unix the open
// is nonblocking, so replacement by a FIFO cannot hang before this check runs.
func openRegularSource(path string, expected os.FileInfo) (*os.File, os.FileInfo, error) {
	if !expected.Mode().IsRegular() {
		return nil, nil, unsupportedSourceType(path, expected.Mode())
	}
	f, err := openSourceFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open input %q: %w", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		return nil, nil, errors.Join(fmt.Errorf("stat open input %q: %w", path, err), f.Close())
	}
	if !info.Mode().IsRegular() {
		return nil, nil, errors.Join(unsupportedSourceType(path, info.Mode()), f.Close())
	}
	if !os.SameFile(expected, info) {
		return nil, nil, errors.Join(fmt.Errorf("input file %q was replaced while being opened", path), f.Close())
	}
	return f, info, nil
}
