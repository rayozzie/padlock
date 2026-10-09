// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"archive/tar"
	"errors"
	"fmt"
	"strings"
)

// ErrSparseTarEntry identifies an archive entry that Padlock cannot safely read.
var ErrSparseTarEntry = errors.New("sparse TAR entries are not supported")

// RejectSparseTarEntry must be called after tar.Reader.Next and before reading
// or skipping an entry. archive/tar synthesizes zero bytes for sparse holes, so
// a tiny archive can otherwise allocate or write an arbitrarily large payload.
// Padlock's writer never emits sparse entries. PAX sparse files have a regular
// typeflag, so checking TypeGNUSparse alone does not detect them.
func RejectSparseTarEntry(header *tar.Header) error {
	if header.Typeflag == tar.TypeGNUSparse {
		return fmt.Errorf("%w: %q", ErrSparseTarEntry, header.Name)
	}
	for key := range header.PAXRecords {
		if strings.HasPrefix(key, "GNU.sparse.") {
			return fmt.Errorf("%w: %q", ErrSparseTarEntry, header.Name)
		}
	}
	return nil
}
