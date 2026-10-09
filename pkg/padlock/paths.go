// Copyright 2025 Ray Ozzie. All rights reserved.

package padlock

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

type resolvedDirectory struct {
	path      string
	info      os.FileInfo   // nil if the directory does not exist yet
	ancestors []os.FileInfo // starts with the nearest existing directory
	missing   []string      // normalized components below that directory, parent first
}

// resolveSeparateDirectories validates the entire set before the caller can
// clear or create any output. Return resolved paths so subsequent operations
// use the same paths we checked, including symlinks followed by .. components.
func resolveSeparateDirectories(inputDirs, outputDirs []string) ([]string, []string, error) {
	inputs := make([]resolvedDirectory, len(inputDirs))
	outputs := make([]resolvedDirectory, len(outputDirs))
	for i, path := range inputDirs {
		dir, err := resolveDirectory(path, false)
		if err != nil {
			return nil, nil, fmt.Errorf("cannot resolve input directory %q: %w", path, err)
		}
		inputs[i] = dir
	}
	for i, path := range outputDirs {
		dir, err := resolveDirectory(path, true)
		if err != nil {
			return nil, nil, fmt.Errorf("cannot resolve output directory %q: %w", path, err)
		}
		outputs[i] = dir
	}

	for i, input := range inputs {
		for j, output := range outputs {
			if directoriesOverlap(input, output) {
				return nil, nil, fmt.Errorf("input and output directories overlap: %q and %q; choose separate directories", inputDirs[i], outputDirs[j])
			}
		}
	}
	for i, first := range outputs {
		for j := i + 1; j < len(outputs); j++ {
			if directoriesOverlap(first, outputs[j]) {
				return nil, nil, fmt.Errorf("output directories overlap or have ambiguous names: %q and %q; choose separate directories", outputDirs[i], outputDirs[j])
			}
		}
	}

	resolvedInputs := make([]string, len(inputs))
	resolvedOutputs := make([]string, len(outputs))
	for i, dir := range inputs {
		resolvedInputs[i] = dir.path
	}
	for i, dir := range outputs {
		resolvedOutputs[i] = dir.path
	}
	return resolvedInputs, resolvedOutputs, nil
}

func directoriesOverlap(first, second resolvedDirectory) bool {
	if directoryContains(first.info, second.ancestors) || directoryContains(second.info, first.ancestors) {
		return true
	}
	// Two missing directories can overlap only below the same existing parent.
	// Compare components, not string prefixes: "out" and "out-other" are separate.
	if first.info != nil || second.info != nil || !os.SameFile(first.ancestors[0], second.ancestors[0]) {
		return false
	}
	for i := 0; i < min(len(first.missing), len(second.missing)); i++ {
		if first.missing[i] != second.missing[i] {
			return false
		}
	}
	return true
}

// Compare filesystem identities instead of path prefixes. This also recognizes
// differently cased names on case-insensitive filesystems and directory aliases.
func directoryContains(parent os.FileInfo, ancestors []os.FileInfo) bool {
	if parent == nil {
		return false
	}
	for _, ancestor := range ancestors {
		if os.SameFile(parent, ancestor) {
			return true
		}
	}
	return false
}

func resolveDirectory(path string, allowMissing bool) (resolvedDirectory, error) {
	if path == "" {
		return resolvedDirectory{}, fmt.Errorf("directory path is empty")
	}

	// Missing outputs are resolved through their nearest existing ancestor.
	// Do not clean the path before EvalSymlinks: link/.. must follow the link
	// before applying .., just as filesystem operations do.
	current := path
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			resolved, err = filepath.Abs(resolved)
			if err != nil {
				return resolvedDirectory{}, err
			}
			dir := resolvedDirectory{path: resolved}
			for ancestor := resolved; ; ancestor = filepath.Dir(ancestor) {
				info, err := os.Stat(ancestor)
				if err != nil {
					return resolvedDirectory{}, err
				}
				if !info.IsDir() {
					return resolvedDirectory{}, fmt.Errorf("not a directory: %q", ancestor)
				}
				dir.ancestors = append(dir.ancestors, info)
				if filepath.Dir(ancestor) == ancestor {
					break
				}
			}
			if len(missing) == 0 {
				dir.info = dir.ancestors[0]
			}
			for i := len(missing) - 1; i >= 0; i-- {
				if runtime.GOOS == "windows" {
					if err := validateWindowsNewDirectoryName(missing[i]); err != nil {
						return resolvedDirectory{}, err
					}
				}
				dir.path = filepath.Join(dir.path, missing[i])
				// We cannot query a directory that does not exist. Conservatively
				// reject case/Unicode aliases without creating filesystem probes.
				// Actual paths keep their spelling; existing directories use identity.
				key := norm.NFD.String(cases.Fold().String(norm.NFD.String(missing[i])))
				dir.missing = append(dir.missing, key)
			}
			return dir, nil
		}
		if !allowMissing || !os.IsNotExist(err) {
			return resolvedDirectory{}, err
		}

		// Strip trailing separators only while looking for a missing component.
		// A dangling symlink is an error, not a directory we may later create.
		for len(current) > 0 && os.IsPathSeparator(current[len(current)-1]) {
			current = current[:len(current)-1]
		}
		if _, statErr := os.Lstat(current); !os.IsNotExist(statErr) {
			return resolvedDirectory{}, err
		}
		parent, name := filepath.Split(current)
		if name == "" || name == "." || name == ".." {
			return resolvedDirectory{}, err
		}
		missing = append(missing, name)
		if parent == "" {
			parent = "."
		}
		current = parent
	}
}

// Windows may assign an 8.3 alias when another output is created. Its spelling
// cannot be predicted safely during read-only preflight. Existing short names
// are resolved normally; reject new names that resemble these aliases, along
// with trailing dots/spaces whose interpretation varies by Windows namespace.
func validateWindowsNewDirectoryName(name string) error {
	if strings.TrimRight(name, ". ") != name {
		return fmt.Errorf("new Windows output directory component %q ends in a dot or space; use an unambiguous name", name)
	}
	base, extension, _ := strings.Cut(name, ".")
	if strings.Contains(base, "~") && utf8.RuneCountInString(base) <= 8 && utf8.RuneCountInString(extension) <= 3 {
		return fmt.Errorf("new Windows output directory component %q resembles an 8.3 alias; use the full directory name", name)
	}
	return nil
}
