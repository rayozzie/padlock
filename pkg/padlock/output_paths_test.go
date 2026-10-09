// Copyright 2025 Ray Ozzie. All rights reserved.

package padlock

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rayozzie/padlock/pkg/pad"
)

func overlappingOutputFixture(t *testing.T, relation string) (root, input, first, second string) {
	t.Helper()
	root = t.TempDir()
	input = filepath.Join(root, "input")
	writePathFixture(t, filepath.Join(input, "source.txt"), "irreplaceable source")
	first = filepath.Join(root, "output")
	second = first
	switch relation {
	case "existing_same", "existing_nested", "new_child", "symlink", "symlink_dotdot", "case_existing", "unicode_existing":
		if relation == "unicode_existing" {
			first = filepath.Join(root, "caf\u00e9")
		}
		writePathFixture(t, filepath.Join(first, "keep.txt"), "old output")
		second = first
		if relation == "existing_nested" {
			second = filepath.Join(first, "child")
			writePathFixture(t, filepath.Join(second, "keep.txt"), "nested output")
		} else if relation == "new_child" {
			second = filepath.Join(first, "new", "child")
		} else if relation == "symlink" {
			second = filepath.Join(root, "alias")
			linkPathFixture(t, first, second)
		} else if relation == "symlink_dotdot" {
			child := filepath.Join(first, "child")
			if err := os.Mkdir(child, 0700); err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(root, "alias")
			linkPathFixture(t, child, alias)
			second = alias + string(filepath.Separator) + ".."
		} else if relation == "case_existing" || relation == "unicode_existing" {
			second = filepath.Join(root, "OUTPUT")
			if relation == "unicode_existing" {
				second = filepath.Join(root, "CAFE\u0301")
			}
			a, err := os.Stat(first)
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.Stat(second)
			if err != nil || !os.SameFile(a, b) {
				t.Skip("filesystem distinguishes these existing names")
			}
		}
	case "missing_same":
	case "missing_nested":
		second = filepath.Join(first, "new", "child")
	case "relative":
		t.Chdir(root)
		first, second = "output", "./output"
	case "trailing_separator":
		second += string(filepath.Separator)
	case "missing_case":
		second = filepath.Join(root, "OUTPUT")
	case "missing_case_nested":
		second = filepath.Join(root, "OUTPUT", "child")
	case "missing_unicode", "missing_unicode_nested":
		first = filepath.Join(root, "caf\u00e9")
		second = filepath.Join(root, "CAFE\u0301")
		if relation == "missing_unicode_nested" {
			second = filepath.Join(second, "child")
		}
	case "missing_case_fold":
		first, second = filepath.Join(root, "Stra\u00dfe"), filepath.Join(root, "STRASSE")
	case "symlink_missing", "symlink_dotdot_missing":
		parent := filepath.Join(root, "physical")
		if err := os.MkdirAll(filepath.Join(parent, "child"), 0700); err != nil {
			t.Fatal(err)
		}
		alias := filepath.Join(root, "alias")
		first = filepath.Join(parent, "future", "output")
		if relation == "symlink_missing" {
			linkPathFixture(t, parent, alias)
			second = filepath.Join(alias, "future")
		} else {
			linkPathFixture(t, filepath.Join(parent, "child"), alias)
			second = alias + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Join("future", "output")
		}
	default:
		t.Fatalf("unknown relation %s", relation)
	}
	return
}

func TestEncodeRejectsOverlappingOutputDirectories(t *testing.T) {
	for _, relation := range []string{
		"existing_same", "existing_nested", "new_child", "missing_same", "missing_nested", "relative", "trailing_separator",
		"symlink", "symlink_dotdot", "symlink_missing", "symlink_dotdot_missing", "case_existing", "unicode_existing",
		"missing_case", "missing_case_nested", "missing_unicode", "missing_unicode_nested", "missing_case_fold",
	} {
		for _, reverse := range []bool{false, true} {
			for _, clear := range []bool{false, true} {
				for _, dryRun := range []bool{false, true} {
					for _, archive := range []bool{false, true} {
						t.Run(fmt.Sprintf("%s/reverse=%t/clear=%t/dry=%t/tar=%t", relation, reverse, clear, dryRun, archive), func(t *testing.T) {
							root, input, first, second := overlappingOutputFixture(t, relation)
							if reverse {
								first, second = second, first
							}
							// Earlier, unrelated destinations must remain unchanged too.
							existing, absent := filepath.Join(root, "earlier"), filepath.Join(root, "not-created")
							writePathFixture(t, filepath.Join(existing, "keep.txt"), "previous backup")
							outputs := []string{existing, absent, first, second}
							before := snapshotDirectoryTree(t, root)
							rng := &encodeFaultRNG{fault: func(int) error { return errors.New("output overlap guard was bypassed") }}
							err := EncodeDirectory(context.Background(), EncodeConfig{
								InputDir: input, OutputDir: existing, OutputDirs: outputs, N: 4, K: 2,
								Format: FormatBin, ChunkSize: 4096, RNG: rng, ArchiveCollections: archive,
								Compression: CompressionGzip, ClearIfNotEmpty: clear, SizeOnly: dryRun,
							})
							if err == nil || !strings.Contains(err.Error(), "output directories overlap") || !strings.Contains(err.Error(), fmt.Sprintf("%q", first)) || !strings.Contains(err.Error(), fmt.Sprintf("%q", second)) {
								t.Errorf("expected overlap error identifying both outputs, got %v", err)
							}
							if after := snapshotDirectoryTree(t, root); !maps.Equal(before, after) {
								t.Error("rejected output layout changed files, directories, or links")
							}
							if rng.reads != 0 {
								t.Error("invalid output layout consumed pad randomness")
							}
						})
					}
				}
			}
		}
	}
}

func TestWindowsNewOutputDirectoryNames(t *testing.T) {
	for _, name := range []string{"BACKUP~1", "LONGBA~2.TAR", "output.", "output "} {
		t.Run("reject/"+name, func(t *testing.T) {
			if err := validateWindowsNewDirectoryName(name); err == nil {
				t.Fatalf("accepted ambiguous new Windows directory name %q", name)
			}
		})
	}
	for _, name := range []string{"backup", ".hidden", "long-backup~old", "backup~1.long"} {
		t.Run("accept/"+name, func(t *testing.T) {
			if err := validateWindowsNewDirectoryName(name); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEncodeOutputCountBeforePreparation(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(fmt.Sprintf("dry=%t", dryRun), func(t *testing.T) {
			root := t.TempDir()
			input, first, second, third := filepath.Join(root, "input"), filepath.Join(root, "first"), filepath.Join(root, "second"), filepath.Join(root, "third")
			writePathFixture(t, filepath.Join(input, "source"), "source")
			writePathFixture(t, filepath.Join(first, "keep"), "previous backup")
			before := snapshotDirectoryTree(t, root)
			err := EncodeDirectory(context.Background(), EncodeConfig{
				InputDir: input, OutputDir: first, OutputDirs: []string{first, second, third},
				N: 2, K: 2, Format: FormatBin, ChunkSize: 4096, RNG: pad.NewCryptoRand(),
				ClearIfNotEmpty: true, SizeOnly: dryRun,
			})
			if err == nil || !strings.Contains(err.Error(), "number of output directories (3) does not match number of collections (2)") {
				t.Errorf("expected output count error, got %v", err)
			}
			if after := snapshotDirectoryTree(t, root); !maps.Equal(before, after) {
				t.Error("output count mismatch changed destinations")
			}
		})
	}
}

func TestDisjointOutputDirectoriesRoundTrip(t *testing.T) {
	for _, relation := range []string{"similar_prefix", "shared_missing_parent", "separate_aliases", "existing_case_distinct", "existing_unicode_distinct"} {
		for _, archive := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/tar=%t", relation, archive), func(t *testing.T) {
				root := t.TempDir()
				input := filepath.Join(root, "input")
				writePathFixture(t, filepath.Join(input, "source"), "separate collections remain recoverable")
				first, second := filepath.Join(root, "output"), filepath.Join(root, "output-other")
				switch relation {
				case "shared_missing_parent":
					first, second = filepath.Join(root, "future", "first"), filepath.Join(root, "future", "second")
				case "separate_aliases":
					for _, name := range []string{"left", "right"} {
						if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
							t.Fatal(err)
						}
						linkPathFixture(t, name, filepath.Join(root, name+"-alias"))
					}
					first, second = filepath.Join(root, "left-alias", "new"), filepath.Join(root, "right-alias", "new")
				case "existing_case_distinct", "existing_unicode_distinct":
					second = filepath.Join(root, "OUTPUT")
					if relation == "existing_unicode_distinct" {
						first, second = filepath.Join(root, "caf\u00e9"), filepath.Join(root, "cafe\u0301")
					}
					for _, path := range []string{first, second} {
						if err := os.MkdirAll(path, 0700); err != nil {
							t.Fatal(err)
						}
					}
					a, err := os.Stat(first)
					if err != nil {
						t.Fatal(err)
					}
					b, err := os.Stat(second)
					if err != nil {
						t.Fatal(err)
					}
					if os.SameFile(a, b) {
						t.Skip("filesystem aliases these names")
					}
				}
				cfg := EncodeConfig{
					InputDir: input, OutputDir: first, OutputDirs: []string{first, second},
					N: 2, K: 2, Format: FormatPNG, ChunkSize: 4096, RNG: pad.NewCryptoRand(),
					Compression: CompressionGzip, ArchiveCollections: archive, ClearIfNotEmpty: true, SizeOnly: true,
				}
				before := snapshotDirectoryTree(t, root)
				if err := EncodeDirectory(context.Background(), cfg); err != nil {
					t.Fatal(err)
				}
				if after := snapshotDirectoryTree(t, root); !maps.Equal(before, after) {
					t.Fatal("valid dry run changed directories")
				}
				cfg.SizeOnly = false
				if err := EncodeDirectory(context.Background(), cfg); err != nil {
					t.Fatal(err)
				}
				restored := filepath.Join(root, "restored")
				if err := DecodeDirectory(context.Background(), DecodeConfig{InputDirs: cfg.OutputDirs, OutputDir: restored, Compression: CompressionGzip}); err != nil {
					t.Fatal(err)
				}
				if got, err := os.ReadFile(filepath.Join(restored, "source")); err != nil || string(got) != "separate collections remain recoverable" {
					t.Fatalf("round trip changed contents: %q, %v", got, err)
				}
			})
		}
	}
}
