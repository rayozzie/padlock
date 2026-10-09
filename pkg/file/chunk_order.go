// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"path/filepath"
	"sort"
	"strings"
)

// sortChunkFiles orders numbered BIN/PNG files by their decimal chunk
// number. %04d is a minimum width, so lexical order breaks at chunk 10000.
// Keep unnumbered files (in lexical order after numbered files) and duplicates:
// the decoder must still validate every candidate's embedded header and payload.
func sortChunkFiles(names []string) {
	sort.Slice(names, func(i, j int) bool {
		return chunkFileLess(names[i], names[j])
	})
}

func chunkFileLess(first, second string) bool {
	a, b := chunkFileNumber(first), chunkFileNumber(second)
	if (a != "") != (b != "") {
		return a != ""
	}
	if a != "" && b != "" {
		if len(a) != len(b) {
			return len(a) < len(b)
		}
		if a != b {
			return a < b
		}
	}
	return first < second
}

// chunkFileNumber returns the decimal suffix with leading zeros removed, or an
// empty string for unnumbered files. Comparing digit lengths then digits avoids
// integer overflow, including on 32-bit systems and with malformed huge suffixes.
func chunkFileNumber(path string) string {
	name := filepath.Base(path)
	if _, format, digits := parseChunkFilename(name); format != "" {
		return normalizeChunkNumber(digits)
	}
	// General directory-to-TAR helpers also sort non-collection filenames.
	ext := filepath.Ext(name)
	if !strings.EqualFold(ext, ".bin") && !strings.EqualFold(ext, ".png") {
		return ""
	}
	stem := strings.TrimSuffix(name, ext)
	separator := strings.LastIndexByte(stem, '_')
	if separator < 0 {
		return ""
	}
	digits := stem[separator+1:]
	if digits == "" {
		return ""
	}
	for i := range digits {
		if digits[i] < '0' || digits[i] > '9' {
			return ""
		}
	}
	return normalizeChunkNumber(digits)
}

func normalizeChunkNumber(digits string) string {
	trimmed := strings.TrimLeft(digits, "0")
	if trimmed == "" {
		return "0"
	}
	return trimmed
}
