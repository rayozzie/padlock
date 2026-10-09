// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"context"
	"path"
	"strings"

	"github.com/rayozzie/padlock/pkg/trace"
)

// ParseChunkFilename identifies a chunk candidate by its basename, without
// trusting its contents. The optional IMG prefix and extension are case-insensitive;
// copy decorations after the initial decimal chunk number are accepted. Keep every matching
// candidate (even duplicate numbers or different collection labels) for the
// decoder to validate. AppleDouble sidecars and unrelated files are not chunks.
func ParseChunkFilename(name string) (collection string, format Format) {
	collection, format, _ = parseChunkFilename(name)
	return collection, format
}

func parseChunkFilename(name string) (collection string, format Format, digits string) {
	if strings.HasPrefix(name, "._") || strings.ContainsAny(name, "/\\") {
		return "", "", ""
	}
	ext := path.Ext(name)
	switch strings.ToLower(ext) {
	case ".bin":
		format = FormatBin
	case ".png":
		format = FormatPNG
	default:
		return "", "", ""
	}
	stem := strings.TrimSuffix(name, ext)
	if len(stem) >= 3 && strings.EqualFold(stem[:3], "IMG") {
		stem = stem[3:]
	}
	collection, suffix, found := strings.Cut(stem, "_")
	if !found || !IsCollectionName(collection) {
		return "", "", ""
	}
	n := 0
	for n < len(suffix) && suffix[n] >= '0' && suffix[n] <= '9' {
		n++
	}
	if n == 0 {
		return "", "", ""
	}
	return collection, format, suffix[:n]
}

// Summarize potentially renamed chunks without flooding normal output with
// unrelated files or expected AppleDouble metadata. No contents are opened.
type skippedChunkNames struct {
	count   int
	example string
}

func (s *skippedChunkNames) add(name string) {
	if strings.HasPrefix(path.Base(name), "._") {
		return
	}
	if ext := path.Ext(name); !strings.EqualFold(ext, ".bin") && !strings.EqualFold(ext, ".png") {
		return
	}
	s.count++
	if s.example == "" {
		s.example = name
	}
}

func (s *skippedChunkNames) report(ctx context.Context, location string) {
	if s.count > 0 {
		trace.FromContext(ctx).Infof("Ignored %d BIN/PNG files in %q with unrecognized chunk filenames (for example %q)", s.count, location, s.example)
	}
}
