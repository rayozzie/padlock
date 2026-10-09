// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
)

var directoryArchiveChecks = []struct {
	name string
	run  func(context.Context, string, io.Reader) error
}{
	{"restore", func(ctx context.Context, dest string, r io.Reader) error {
		return DeserializeDirectoryFromStream(ctx, dest, r, false)
	}},
	{"dryrun", func(ctx context.Context, _ string, r io.Reader) error {
		return ValidateDirectoryStream(ctx, r)
	}},
}

func TestDirectoryArchiveWithoutFiles(t *testing.T) {
	for _, directories := range []bool{false, true} {
		var entries []extractionTestEntry
		if directories {
			for _, name := range []string{"parent", "parent/child"} {
				entries = append(entries, extractionTestEntry{header: tar.Header{Name: name, Typeflag: tar.TypeDir}})
			}
		}
		archive := extractionTestTar(t, entries...)
		compressed := extractionTestGzip(t, archive)
		badCRC := bytes.Clone(compressed)
		badCRC[len(badCRC)-8] ^= 1
		badSize := bytes.Clone(compressed)
		badSize[len(badSize)-1] ^= 1
		for _, tc := range []struct {
			name string
			data []byte
			err  error
		}{
			{"complete_tar", archive, nil},
			{"complete_gzip", compressed, nil},
			{"no_data", nil, io.ErrUnexpectedEOF},
			{"no_ending_blocks", archive[:len(archive)-1024], io.ErrUnexpectedEOF},
			{"one_ending_block", archive[:len(archive)-512], io.ErrUnexpectedEOF},
			{"partial_ending_block", archive[:len(archive)-1], io.ErrUnexpectedEOF},
			{"incomplete_tar_in_gzip", extractionTestGzip(t, archive[:len(archive)-512]), io.ErrUnexpectedEOF},
			{"missing_gzip_footer", compressed[:len(compressed)-8], io.ErrUnexpectedEOF},
			{"bad_gzip_crc", badCRC, gzip.ErrChecksum},
			{"bad_gzip_size", badSize, gzip.ErrChecksum},
		} {
			for _, check := range directoryArchiveChecks {
				for _, dataWithEOF := range []bool{false, true} {
					t.Run(fmt.Sprintf("directories=%t/%s/%s/data_with_eof=%t", directories, tc.name, check.name, dataWithEOF), func(t *testing.T) {
						var reader io.Reader = bytes.NewReader(tc.data)
						if dataWithEOF {
							reader = iotest.DataErrReader(reader)
						}
						dest := filepath.Join(t.TempDir(), "restored")
						err := check.run(context.Background(), dest, reader)
						if !errors.Is(err, tc.err) {
							t.Fatalf("got %v, want %v", err, tc.err)
						}
						if check.name == "dryrun" {
							if _, err := os.Stat(dest); !os.IsNotExist(err) {
								t.Fatalf("validation created output: %v", err)
							}
							return
						}
						if tc.err != nil {
							return
						}
						count := 0
						if err := filepath.Walk(dest, func(path string, info os.FileInfo, err error) error {
							if err != nil {
								return err
							}
							if !info.IsDir() {
								return fmt.Errorf("unexpected restored file: %s", path)
							}
							if path != dest {
								count++
							}
							return nil
						}); err != nil {
							t.Fatal(err)
						}
						if count != len(entries) {
							t.Fatalf("restored %d directories, want %d", count, len(entries))
						}
						for _, entry := range entries {
							if info, err := os.Stat(filepath.Join(dest, entry.header.Name)); err != nil || !info.IsDir() {
								t.Fatalf("missing restored directory %s: %v", entry.header.Name, err)
							}
						}
					})
				}
			}
		}
	}
}

func TestDirectoryArchiveRequiresTerminator(t *testing.T) {
	archive := extractionTestTar(t,
		extractionTestEntry{header: tar.Header{Name: "a.txt", Typeflag: tar.TypeReg}, body: "first file\n"},
		// Zero-filled file contents must not be mistaken for ending blocks.
		extractionTestEntry{header: tar.Header{Name: "zeros.bin", Typeflag: tar.TypeReg}, body: string(make([]byte, 1024))},
	)
	for _, cut := range []int{0, 1, 127, 511, 512, 517, 523, 1023, 1024, len(archive) - 1024, len(archive) - 512, len(archive) - 1} {
		for _, compressed := range []bool{false, true} {
			for _, check := range directoryArchiveChecks {
				t.Run(fmt.Sprintf("cut=%d/gzip=%t/%s", cut, compressed, check.name), func(t *testing.T) {
					data := archive[:cut]
					if compressed {
						// A valid gzip ending cannot make an incomplete TAR valid.
						data = extractionTestGzip(t, data)
					}
					dest := filepath.Join(t.TempDir(), "restored")
					err := check.run(context.Background(), dest, bytes.NewReader(data))
					if !errors.Is(err, io.ErrUnexpectedEOF) {
						t.Fatalf("got %v for a %d-byte archive prefix, want unexpected EOF", err, cut)
					}
					for _, fallback := range []string{"decoded_data.txt", "decoded_data.bin", "decoded_output.dat"} {
						if _, err := os.Stat(filepath.Join(dest, fallback)); !os.IsNotExist(err) {
							t.Errorf("truncated archive was recovered as a raw file %s: %v", fallback, err)
						}
					}
				})
			}
		}
	}
}

func TestDirectoryArchiveAcceptsCompleteStreams(t *testing.T) {
	for _, format := range []tar.Format{tar.FormatUSTAR, tar.FormatPAX, tar.FormatGNU} {
		name := "nested/data.bin"
		if format != tar.FormatUSTAR {
			name = "nested/" + strings.Repeat("long-name-", 15) + "data.bin"
		}
		body := string(make([]byte, 2048))
		archive := extractionTestTar(t,
			extractionTestEntry{header: tar.Header{Name: name, Typeflag: tar.TypeReg, Format: format}, body: body},
			extractionTestEntry{header: tar.Header{Name: "empty.txt", Typeflag: tar.TypeReg, Format: format}},
		)
		for _, padding := range []int{0, 4096} {
			for _, compressed := range []bool{false, true} {
				data := append(append([]byte(nil), archive...), make([]byte, padding)...)
				if compressed {
					data = extractionTestGzip(t, data)
				}
				for _, readerKind := range []string{"regular", "one_byte", "data_with_eof"} {
					for _, check := range directoryArchiveChecks {
						t.Run(fmt.Sprintf("%s/padding=%d/gzip=%t/%s/%s", format, padding, compressed, readerKind, check.name), func(t *testing.T) {
							var reader io.Reader = bytes.NewReader(data)
							switch readerKind {
							case "one_byte":
								reader = iotest.OneByteReader(reader)
							case "data_with_eof":
								reader = iotest.DataErrReader(reader)
							}
							dest := filepath.Join(t.TempDir(), "restored")
							if err := check.run(context.Background(), dest, reader); err != nil {
								t.Fatal(err)
							}
							if check.name == "restore" {
								assertExtractionFile(t, filepath.Join(dest, name), body)
								assertExtractionFile(t, filepath.Join(dest, "empty.txt"), "")
							} else if _, err := os.Stat(dest); !os.IsNotExist(err) {
								t.Errorf("validation created output directory: %v", err)
							}
						})
					}
				}
			}
		}
	}
}

func TestDirectoryArchiveChecksGzipEnding(t *testing.T) {
	archive := extractionTestTar(t, extractionTestEntry{
		header: tar.Header{Name: "data.bin", Typeflag: tar.TypeReg}, body: string(make([]byte, 8193)),
	})
	compressed := extractionTestGzip(t, archive)
	badCRC := append([]byte(nil), compressed...)
	badCRC[len(badCRC)-8] ^= 1
	badSize := append([]byte(nil), compressed...)
	badSize[len(badSize)-1] ^= 1
	for _, tc := range []struct {
		name string
		data []byte
		err  error
	}{
		{"no_footer", compressed[:len(compressed)-8], io.ErrUnexpectedEOF},
		{"partial_footer", compressed[:len(compressed)-1], io.ErrUnexpectedEOF},
		{"bad_crc", badCRC, gzip.ErrChecksum},
		{"bad_size", badSize, gzip.ErrChecksum},
	} {
		for _, check := range directoryArchiveChecks {
			t.Run(tc.name+"/"+check.name, func(t *testing.T) {
				err := check.run(context.Background(), filepath.Join(t.TempDir(), "restored"), bytes.NewReader(tc.data))
				if !errors.Is(err, tc.err) {
					t.Fatalf("got %v, want gzip ending error %v", err, tc.err)
				}
			})
		}
	}
}

// Return a one-time error with the last bytes, then EOF. Archive validation must
// preserve this error even when a full final block is delivered in that read.
type archiveEndingErrorReader struct {
	data []byte
	err  error
}

func (r *archiveEndingErrorReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	if len(r.data) == 0 {
		err := r.err
		r.err = io.EOF
		return n, err
	}
	return n, nil
}

func TestDirectoryArchivePreservesEndingReadErrors(t *testing.T) {
	archive := extractionTestTar(t, extractionTestEntry{
		header: tar.Header{Name: "a.txt", Typeflag: tar.TypeReg}, body: "contents",
	})
	failure := errors.New("archive source failed")
	for _, withFinalBytes := range []bool{false, true} {
		for _, check := range directoryArchiveChecks {
			t.Run(fmt.Sprintf("with_final_bytes=%t/%s", withFinalBytes, check.name), func(t *testing.T) {
				var reader io.Reader = &archiveEndingErrorReader{data: archive, err: failure}
				if !withFinalBytes {
					reader = io.MultiReader(bytes.NewReader(archive), &archiveEndingErrorReader{err: failure})
				}
				err := check.run(context.Background(), filepath.Join(t.TempDir(), "restored"), reader)
				if !errors.Is(err, failure) {
					t.Fatalf("got %v, want original source error", err)
				}
			})
		}
	}
}
