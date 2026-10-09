// Copyright 2025 Ray Ozzie. All rights reserved.

package padlock

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rayozzie/padlock/pkg/pad"
)

func writePathFixture(t *testing.T, path string, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func linkPathFixture(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

// Include files, directories, and link targets so rejection must leave the
// entire fixture untouched, including any earlier output in a multi-path call.
func snapshotDirectoryTree(t *testing.T, root string) map[string]string {
	t.Helper()
	entries := make(map[string]string)
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			entries[path] = "directory"
		case entry.Type()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			entries[path] = "link:" + target
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entries[path] = "file:" + string(data)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return entries
}

func assertPathRejection(t *testing.T, root string, before map[string]string, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "input and output directories overlap") {
		t.Errorf("expected directory overlap error, got %v", err)
	}
	if after := snapshotDirectoryTree(t, root); !reflect.DeepEqual(after, before) {
		for path, value := range before {
			if after[path] != value {
				t.Errorf("rejected operation changed or removed %s", path)
			}
		}
		for path := range after {
			if _, existed := before[path]; !existed {
				t.Errorf("rejected operation created %s", path)
			}
		}
	}
}

func TestOperationsRejectOverlappingDirectories(t *testing.T) {
	ctx := context.Background()
	for _, operation := range []string{"encode", "decode"} {
		for _, clear := range []bool{false, true} {
			for _, relation := range []string{
				"same", "output_parent", "output_child", "new_output_child", "relative",
				"output_symlink", "input_symlink", "symlink_parent", "symlink_dotdot", "symlink_dotdot_child", "case_alias", "case_child",
			} {
				t.Run(fmt.Sprintf("%s/clear=%t/%s", operation, clear, relation), func(t *testing.T) {
					root := t.TempDir()
					input := filepath.Join(root, "input")
					writePathFixture(t, filepath.Join(input, "source.txt"), "irreplaceable source data")
					writePathFixture(t, filepath.Join(root, "keep.txt"), "unrelated data")
					output := input
					switch relation {
					case "output_parent":
						output = root
					case "output_child":
						output = filepath.Join(input, "output")
						writePathFixture(t, filepath.Join(output, "keep.txt"), "existing nested data")
					case "new_output_child":
						output = filepath.Join(input, "new", "output")
					case "relative":
						t.Chdir(root)
						input, output = "input", "./input/../input"
					case "output_symlink":
						output = filepath.Join(root, "alias")
						linkPathFixture(t, input, output)
					case "input_symlink":
						input = filepath.Join(root, "alias")
						linkPathFixture(t, output, input)
					case "symlink_parent":
						alias := filepath.Join(root, "alias")
						linkPathFixture(t, input, alias)
						output = filepath.Join(alias, "new", "output")
					case "symlink_dotdot", "symlink_dotdot_child":
						subdir := filepath.Join(input, "subdir")
						if err := os.Mkdir(subdir, 0700); err != nil {
							t.Fatal(err)
						}
						alias := filepath.Join(root, "alias")
						linkPathFixture(t, subdir, alias)
						// Keep the .. component: lexical cleaning changes its meaning.
						output = alias + string(filepath.Separator) + ".."
						if relation == "symlink_dotdot_child" {
							output += string(filepath.Separator) + filepath.Join("new", "output")
						}
					case "case_alias", "case_child":
						output = filepath.Join(root, "INPUT")
						original, err := os.Stat(input)
						if err != nil {
							t.Fatal(err)
						}
						alias, err := os.Stat(output)
						if err != nil || !os.SameFile(original, alias) {
							t.Skip("filesystem is case sensitive")
						}
						if relation == "case_child" {
							output = filepath.Join(output, "new", "output")
						}
					}
					before := snapshotDirectoryTree(t, root)
					var err error
					if operation == "encode" {
						// Stop before writing chunks if validation ever regresses;
						// the snapshot still detects destructive directory preparation.
						rng := &encodeFaultRNG{fault: func(int) error { return errors.New("overlap guard was bypassed") }}
						err = EncodeDirectory(ctx, EncodeConfig{
							InputDir: input, OutputDir: output, N: 2, K: 2,
							Format: FormatBin, ChunkSize: 4096, RNG: rng,
							ArchiveCollections: true, Compression: CompressionGzip, ClearIfNotEmpty: clear,
						})
					} else {
						err = DecodeDirectory(ctx, DecodeConfig{InputDir: input, OutputDir: output, ClearIfNotEmpty: clear})
					}
					assertPathRejection(t, root, before, err)
				})
			}
		}
	}
}

func TestEncodeChecksAllOutputsBeforeChangingAny(t *testing.T) {
	root := t.TempDir()
	input, existing, missing := filepath.Join(root, "input"), filepath.Join(root, "existing"), filepath.Join(root, "new")
	writePathFixture(t, filepath.Join(input, "source.txt"), "source data")
	writePathFixture(t, filepath.Join(existing, "keep.txt"), "existing output data")
	before := snapshotDirectoryTree(t, root)
	err := EncodeDirectory(context.Background(), EncodeConfig{
		InputDir: input, OutputDir: existing, OutputDirs: []string{existing, missing, input},
		N: 3, K: 2, Format: FormatBin, ChunkSize: 4096, RNG: pad.NewCryptoRand(),
		ArchiveCollections: true, Compression: CompressionGzip, ClearIfNotEmpty: true,
	})
	assertPathRejection(t, root, before, err)
}

func TestDecodeChecksAllInputsBeforeChangingOutput(t *testing.T) {
	root := t.TempDir()
	first, output := filepath.Join(root, "first"), filepath.Join(root, "output")
	second := filepath.Join(output, "second")
	writePathFixture(t, filepath.Join(first, "2A2_0001.bin"), "first collection data")
	writePathFixture(t, filepath.Join(second, "2B2_0001.bin"), "second collection data")
	writePathFixture(t, filepath.Join(output, "keep.txt"), "existing output data")
	before := snapshotDirectoryTree(t, root)
	err := DecodeDirectory(context.Background(), DecodeConfig{
		InputDir: first, InputDirs: []string{first, second}, OutputDir: output, ClearIfNotEmpty: true,
	})
	assertPathRejection(t, root, before, err)
}

func TestInvalidDirectoryPathsLeaveOutputsUntouched(t *testing.T) {
	for _, invalid := range []string{"missing_input", "file_input", "file_output_parent", "dangling_output", "symlink_loop"} {
		t.Run(invalid, func(t *testing.T) {
			root := t.TempDir()
			input, output := filepath.Join(root, "input"), filepath.Join(root, "output")
			writePathFixture(t, filepath.Join(input, "source.txt"), "source data")
			writePathFixture(t, filepath.Join(output, "keep.txt"), "existing output data")
			badOutput := filepath.Join(root, "bad-output")
			switch invalid {
			case "missing_input":
				input = filepath.Join(root, "missing")
			case "file_input":
				input = filepath.Join(input, "source.txt")
			case "file_output_parent":
				writePathFixture(t, badOutput, "not a directory")
				badOutput = filepath.Join(badOutput, "child")
			case "dangling_output":
				linkPathFixture(t, filepath.Join(root, "missing"), badOutput)
				badOutput = filepath.Join(badOutput, "child")
			case "symlink_loop":
				linkPathFixture(t, badOutput, badOutput)
			}
			before := snapshotDirectoryTree(t, root)
			if err := EncodeDirectory(context.Background(), EncodeConfig{
				InputDir: input, OutputDir: output, OutputDirs: []string{output, badOutput},
				N: 2, K: 2, Format: FormatBin, ChunkSize: 4096, RNG: pad.NewCryptoRand(), ClearIfNotEmpty: true,
			}); err == nil {
				t.Fatal("encode accepted an invalid directory path")
			}
			if invalid == "missing_input" || invalid == "file_input" {
				if err := DecodeDirectory(context.Background(), DecodeConfig{InputDir: input, OutputDir: output, ClearIfNotEmpty: true}); err == nil {
					t.Fatal("decode accepted an invalid input directory")
				}
			}
			if after := snapshotDirectoryTree(t, root); !reflect.DeepEqual(after, before) {
				t.Fatal("invalid paths caused an output directory to be modified")
			}
		})
	}
}

func TestSeparateDirectoryPathsRoundTrip(t *testing.T) {
	ctx := context.Background()
	for _, archive := range []bool{false, true} {
		for _, multiple := range []bool{false, true} {
			t.Run(fmt.Sprintf("tar=%t/multiple=%t", archive, multiple), func(t *testing.T) {
				root := t.TempDir()
				t.Chdir(root)
				// Similar prefixes must not count as directory containment.
				writePathFixture(t, "data/source.txt", "source survives clearing unrelated outputs")
				writePathFixture(t, "data-backup/keep.txt", "old output to clear")
				writePathFixture(t, "data-restored/keep.txt", "old restore to clear")
				linkPathFixture(t, "data", "input-alias")
				linkPathFixture(t, "data-backup", "output-alias")
				linkPathFixture(t, "data-restored", "restore-alias")
				cfg := EncodeConfig{
					InputDir: "input-alias", OutputDir: "output-alias",
					N: 2, K: 2, Format: FormatBin, ChunkSize: 4096, RNG: pad.NewCryptoRand(),
					Compression: CompressionGzip, ArchiveCollections: archive, ClearIfNotEmpty: true,
				}
				if multiple {
					// Exercise a new directory below a symlinked existing parent.
					if err := os.Mkdir("other", 0700); err != nil {
						t.Fatal(err)
					}
					linkPathFixture(t, "other", "other-alias")
					cfg.OutputDirs = []string{cfg.OutputDir, filepath.Join("other-alias", "new", "second") + string(filepath.Separator)}
				}
				if err := EncodeDirectory(ctx, cfg); err != nil {
					t.Fatal(err)
				}
				decode := DecodeConfig{InputDir: cfg.OutputDir, OutputDir: "restore-alias", Compression: CompressionGzip, ClearIfNotEmpty: true}
				if multiple {
					decode.InputDirs = cfg.OutputDirs
				}
				if err := DecodeDirectory(ctx, decode); err != nil {
					t.Fatal(err)
				}
				for _, dir := range []string{"data", "data-restored"} {
					contents, err := os.ReadFile(filepath.Join(dir, "source.txt"))
					if err != nil || string(contents) != "source survives clearing unrelated outputs" {
						t.Fatalf("data in %s changed: %q, err=%v", dir, contents, err)
					}
				}
				for _, path := range []string{"data-backup/keep.txt", "data-restored/keep.txt"} {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Errorf("-clear failed to remove %s: %v", path, err)
					}
				}
			})
		}
	}
}

func TestDryRunDoesNotPrepareOverlappingOutputs(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	input, encoded := filepath.Join(root, "input"), filepath.Join(root, "encoded")
	writePathFixture(t, filepath.Join(input, "source.txt"), "source data")
	cfg := EncodeConfig{
		InputDir: input, OutputDir: encoded, N: 2, K: 2, Format: FormatBin,
		ChunkSize: 4096, RNG: pad.NewCryptoRand(), Compression: CompressionGzip, ArchiveCollections: true,
	}
	if err := EncodeDirectory(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	before := snapshotDirectoryTree(t, root)
	cfg.OutputDir, cfg.SizeOnly, cfg.ClearIfNotEmpty = input, true, true
	if err := EncodeDirectory(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err := DecodeDirectory(ctx, DecodeConfig{
		InputDir: encoded, OutputDir: encoded, SizeOnly: true, ClearIfNotEmpty: true, Compression: CompressionGzip,
	}); err != nil {
		t.Fatal(err)
	}
	if after := snapshotDirectoryTree(t, root); !reflect.DeepEqual(after, before) {
		t.Fatal("dry run modified files or directories")
	}
}
