// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"

	"github.com/rayozzie/padlock/pkg/trace"
)

func findTarCollection(ctx context.Context, tarPath string) (collection Collection, err error) {
	f, err := openCollectionFile(tarPath)
	if err != nil {
		return Collection{}, fmt.Errorf("open collection archive %q: %w", tarPath, err)
	}
	defer func() {
		err = errors.Join(err, f.Close())
		if err != nil {
			err = fmt.Errorf("read collection archive %q: %w", tarPath, err)
		}
	}()
	return probeTarCollection(ctx, f, tarPath)
}

// probeTarCollection uses names only to identify a candidate; decoding still
// validates the embedded chunk headers and payloads. Keep the seekable reader
// intact so tar.Reader.Next can skip ordinary bodies without copying them.
// Sparse entries must be rejected before Next can skip their contents.
func probeTarCollection(ctx context.Context, source io.ReadSeeker, tarPath string) (Collection, error) {
	name := strings.TrimSuffix(filepath.Base(tarPath), ".tar")
	namedArchive := IsCollectionName(name)
	log := trace.FromContext(ctx).WithPrefix("COLLECTION")
	r := tar.NewReader(source)
	var skipped skippedChunkNames
	for {
		if err := ctx.Err(); err != nil {
			return Collection{}, err
		}
		header, err := r.Next()
		if err == io.EOF {
			skipped.report(ctx, tarPath)
			return Collection{}, nil
		}
		if err != nil {
			return Collection{}, err
		}
		if err := RejectSparseTarEntry(header); err != nil {
			return Collection{}, err
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			continue
		}
		// TAR member paths always use forward slashes, including on Windows.
		member := path.Base(header.Name)
		memberCollection, format := ParseChunkFilename(member)
		if format == "" {
			skipped.add(header.Name)
			log.Debugf("Skipping non-chunk TAR entry: %s", header.Name)
			continue
		}
		if !namedArchive {
			name = memberCollection
		}
		return Collection{Name: name, Path: tarPath, Format: format}, nil
	}
}
