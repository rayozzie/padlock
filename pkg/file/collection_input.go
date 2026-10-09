// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// ErrInvalidCollectionFile must not be treated as an unrelated archive during
// discovery, even when enough other collections have already been found.
var ErrInvalidCollectionFile = errors.New("invalid collection file")

func inspectCollectionFile(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("%w: inspect %q: %w", ErrInvalidCollectionFile, path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, collectionFileTypeError(path, info.Mode())
	}
	return info, nil
}

func collectionFileTypeError(path string, mode os.FileMode) error {
	return fmt.Errorf("%w: %q is not a regular file (%s)", ErrInvalidCollectionFile, path, mode.Type())
}

func openCollectionFile(path string) (*os.File, error) {
	info, err := inspectCollectionFile(path)
	if err != nil {
		return nil, err
	}
	return openCheckedCollectionFile(path, info)
}

// Recheck the opened object against Lstat. On Unix, a nonblocking/no-follow open
// prevents a substituted FIFO or leaf symlink from hanging before f.Stat runs.
// This is not a snapshot: files may still be modified through another handle.
func openCheckedCollectionFile(path string, expected os.FileInfo) (*os.File, error) {
	if !expected.Mode().IsRegular() {
		return nil, collectionFileTypeError(path, expected.Mode())
	}
	f, err := openSourceFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: open %q: %w", ErrInvalidCollectionFile, path, err)
	}
	actual, err := f.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("%w: stat %q: %w", ErrInvalidCollectionFile, path, err), f.Close())
	}
	if !actual.Mode().IsRegular() {
		return nil, errors.Join(collectionFileTypeError(path, actual.Mode()), f.Close())
	}
	if !os.SameFile(expected, actual) {
		return nil, errors.Join(fmt.Errorf("%w: %q was replaced while being opened", ErrInvalidCollectionFile, path), f.Close())
	}
	return f, nil
}

func readCollectionFile(path string, format Format) ([]byte, error) {
	f, err := openCollectionFile(path)
	if err != nil {
		return nil, err
	}
	var data []byte
	if format == FormatPNG {
		data, err = ExtractDataFromPNG(f)
	} else {
		data, err = io.ReadAll(f)
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return nil, fmt.Errorf("read collection file %q: %w", path, err)
	}
	return data, nil
}
