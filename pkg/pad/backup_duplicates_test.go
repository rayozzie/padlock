// Copyright 2025 Ray Ozzie. All rights reserved.

package pad

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

// Expose exactly one already loaded frame, like the filesystem chunk adapter.
// Reuse the backing array as soon as another frame is requested to exercise the
// preflight code's borrowed-buffer lifetime contract.
type preflightBufferedReader struct {
	frames [][]byte
	buffer []byte
	offset int
}

func (r *preflightBufferedReader) Read(p []byte) (int, error) {
	if r.offset == len(r.buffer) {
		if len(r.frames) == 0 {
			return 0, io.EOF
		}
		r.buffer = append(r.buffer[:0], r.frames[0]...)
		r.frames = r.frames[1:]
		r.offset = 0
	}
	n := copy(p, r.buffer[r.offset:])
	r.offset += n
	return n, nil
}

func (r *preflightBufferedReader) BufferedChunk() []byte { return r.buffer[r.offset:] }
func (r *preflightBufferedReader) Path() string          { return "test-input.tar" }

func TestBackupPreflightDuplicatePayloads(t *testing.T) {
	ctx := context.Background()
	encoder, err := NewPadForEncode(ctx, 5, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Repeat([]byte("duplicate preflight"), 31)
	frames := encodeBackupChunks(t, encoder, 64, want)
	for _, buffered := range []string{"none", "all", "first", "duplicate"} {
		for _, legacy := range []bool{false, true} {
			for _, damage := range []string{"none", "payload", "size", "truncated", "read_error"} {
				t.Run(fmt.Sprintf("buffered=%s/legacy=%t/%s", buffered, legacy, damage), func(t *testing.T) {
					failure := errors.New("injected duplicate read failure")
					var readers []io.Reader
					// The unused E duplicate follows all the first copies. Their
					// buffers may change before the duplicate is replayed by Decode.
					for index, name := range []string{"3A5", "3B5", "3C5", "3D5", "3E5", "3E5"} {
						var copyFrames [][]byte
						for _, frame := range frames[name] {
							frame = bytes.Clone(frame)
							if legacy {
								frame = replaceBackupID(frame, "")
							}
							copyFrames = append(copyFrames, frame)
						}
						if index == 5 {
							switch damage {
							case "payload":
								copyFrames[0][len(copyFrames[0])-1] ^= 1
							case "size":
								fields := strings.Split(string(copyFrames[0][1:1+int(copyFrames[0][0])]), ":")
								fields[2] = "63"
								header := strings.Join(fields, ":")
								copyFrames[0] = append(append([]byte{byte(len(header))}, header...), copyFrames[0][1+int(copyFrames[0][0]):]...)
							case "truncated", "read_error":
								copyFrames = [][]byte{copyFrames[0][:len(copyFrames[0])-1]}
							}
						}
						var reader io.Reader = bytes.NewReader(bytes.Join(copyFrames, nil))
						if buffered == "all" || (buffered == "first" && index != 5) || (buffered == "duplicate" && index == 5) {
							reader = &preflightBufferedReader{frames: copyFrames}
						}
						if damage == "read_error" && index == 5 {
							reader = io.MultiReader(reader, payloadFailure{failure})
						}
						readers = append(readers, reader)
					}
					prepared, err := CheckBackupIDs(readers)
					if damage != "none" {
						if err == nil || prepared != nil || !strings.Contains(err.Error(), "3E5") {
							t.Fatalf("preflight accepted first-chunk damage: %v", err)
						}
						if damage == "read_error" && !errors.Is(err, failure) {
							t.Fatalf("original read failure lost: %v", err)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					decoder, err := NewPadForDecode(ctx, len(prepared))
					if err != nil {
						t.Fatal(err)
					}
					var restored bytes.Buffer
					if err := decoder.Decode(ctx, prepared, &restored); err != nil || !bytes.Equal(restored.Bytes(), want) {
						t.Fatalf("preflight lost/reused replayed data: %v", err)
					}
				})
			}
		}
	}
}

func TestBackupPreflightKeepsInputPath(t *testing.T) {
	for _, first := range []bool{false, true} {
		t.Run(fmt.Sprintf("first=%t", first), func(t *testing.T) {
			var readers []io.Reader
			for _, name := range []string{"2A2", "2B2"} {
				var frames [][]byte
				for chunk := 1; chunk <= 2; chunk++ {
					number := chunk
					if name == "2A2" && ((first && chunk == 1) || (!first && chunk == 2)) {
						number++
					}
					header := fmt.Sprintf("%s:%d:1", name, number)
					frames = append(frames, append(append([]byte{byte(len(header))}, header...), 0))
				}
				readers = append(readers, &preflightBufferedReader{frames: frames})
			}
			prepared, err := CheckBackupIDs(readers)
			if !first {
				if err != nil {
					t.Fatal(err)
				}
				decoder, err := NewPadForDecode(context.Background(), 2)
				if err != nil {
					t.Fatal(err)
				}
				err = decoder.Decode(context.Background(), prepared, io.Discard)
				for _, detail := range []string{"chunk number mismatch", "2A2", "input 1", "test-input.tar"} {
					if err == nil || !strings.Contains(err.Error(), detail) {
						t.Fatalf("decode diagnostic missing %s: %v", detail, err)
					}
				}
				return
			}
			for _, detail := range []string{"chunk number mismatch", "2A2", "input 1", "test-input.tar"} {
				if err == nil || !strings.Contains(err.Error(), detail) {
					t.Fatalf("preflight diagnostic missing %s: %v", detail, err)
				}
			}
		})
	}
}
