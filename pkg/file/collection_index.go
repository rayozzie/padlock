// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"sort"

	"github.com/rayozzie/padlock/pkg/trace"
)

type tarChunkLocation struct {
	name         string
	offset, size int64
}

// Keep skip failures associated with the body being skipped, including errors
// delivered alongside its final byte (which io.CopyN inside tar can otherwise
// suppress). Header/padding failures retain header diagnostics instead.
type tarIndexSource struct {
	io.ReadSeeker
	position  int64
	entry     tarChunkLocation
	archive   string
	operation string
	failure   error
}

func (s *tarIndexSource) remember(err error) error {
	if err == nil || err == io.EOF {
		return err
	}
	if s.position < s.entry.offset+s.entry.size {
		s.failure = fmt.Errorf("%s TAR entry %q from %q: %w", s.operation, s.entry.name, s.archive, err)
	} else {
		s.failure = fmt.Errorf("read TAR header from %q: %w", s.archive, err)
	}
	return s.failure
}

func (s *tarIndexSource) Read(p []byte) (int, error) {
	if s.failure != nil {
		return 0, s.failure
	}
	n, err := s.ReadSeeker.Read(p)
	if err == io.EOF && s.position+int64(n) < s.entry.offset+s.entry.size {
		err = io.ErrUnexpectedEOF
	}
	err = s.remember(err)
	s.position += int64(n)
	return n, err
}

func (s *tarIndexSource) Seek(offset int64, whence int) (int64, error) {
	if s.failure != nil {
		return 0, s.failure
	}
	position, err := s.ReadSeeker.Seek(offset, whence)
	if err != nil {
		return position, s.remember(err)
	}
	s.position = position
	return position, nil
}

// indexTarChunks records metadata only. tar.Reader skips ordinary bodies using
// Seek (and checks their last byte); sparse entries are rejected before any skip.
// Keep duplicate numbers/names, rather than replacing an earlier member in a map.
// Embedded headers and payloads are still validated when chunks are consumed.
func indexTarChunks(ctx context.Context, source io.ReadSeeker, archive string) ([]tarChunkLocation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	size, err := source.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("measure TAR archive %q: %w", archive, err)
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind TAR archive %q: %w", archive, err)
	}
	log := trace.FromContext(ctx).WithPrefix("TAR-INDEX")
	var chunks []tarChunkLocation
	var skipped skippedChunkNames
	defer skipped.report(ctx, archive)
	tracked := &tarIndexSource{ReadSeeker: source, archive: archive}
	r := tar.NewReader(tracked)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		h, err := r.Next()
		if tracked.failure != nil {
			return nil, tracked.failure
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read TAR header from %q: %w", archive, err)
		}
		if err := RejectSparseTarEntry(h); err != nil {
			return nil, fmt.Errorf("read TAR entry %q from %q: %w", h.Name, archive, err)
		}
		_, format := ParseChunkFilename(path.Base(h.Name))
		if format != "" && h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			return nil, fmt.Errorf("read TAR entry %q from %q: chunk is not a regular file", h.Name, archive)
		}
		offset := tracked.position
		bodySize := h.Size
		switch h.Typeflag {
		case tar.TypeLink, tar.TypeSymlink, tar.TypeChar, tar.TypeBlock, tar.TypeDir, tar.TypeFifo:
			bodySize = 0 // archive/tar treats these as header-only entries.
		}
		// Check before advancing so truncated bodies retain entry diagnostics,
		// rather than being mislabeled as an error in the following header.
		// Subtraction avoids overflow for malicious sizes and on 32-bit hosts.
		operation := "skip"
		if format != "" {
			operation = "read"
		}
		if offset < 0 || offset > size || bodySize < 0 || bodySize > size-offset {
			return nil, fmt.Errorf("%s TAR entry %q from %q: declared body extends past archive: %w", operation, h.Name, archive, io.ErrUnexpectedEOF)
		}
		tracked.entry = tarChunkLocation{name: h.Name, offset: offset, size: bodySize}
		tracked.operation = operation
		if format == "" {
			skipped.add(h.Name)
			log.Debugf("Skipping non-chunk TAR entry: %s", h.Name)
			continue
		}
		chunks = append(chunks, tarChunkLocation{name: h.Name, offset: offset, size: bodySize})
	}
	// Stable ordering also preserves multiple occurrences of the exact same name.
	sort.SliceStable(chunks, func(i, j int) bool {
		return chunkFileLess(chunks[i].name, chunks[j].name)
	})
	return chunks, nil
}

func (cr *CollectionReader) readIndexedTarChunk(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cr.ChunkIndex > len(cr.tarChunks) {
		return nil, io.EOF
	}
	entry := cr.tarChunks[cr.ChunkIndex-1]
	// SectionReader bounds reads to this member and uses the same open handle as
	// indexing. Never allocate the untrusted declared size in advance.
	data, err := io.ReadAll(io.NewSectionReader(cr.tarFile, entry.offset, entry.size))
	if err == nil && int64(len(data)) != entry.size {
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		return nil, fmt.Errorf("read TAR entry %q from %q (read %d bytes): %w", entry.name, cr.Collection.Path, len(data), err)
	}
	if _, format := ParseChunkFilename(path.Base(entry.name)); format == FormatPNG {
		data, err = ExtractDataFromPNG(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("decode PNG entry %q from TAR %q: %w", entry.name, cr.Collection.Path, err)
		}
	}
	cr.ChunkIndex++
	return data, nil
}
