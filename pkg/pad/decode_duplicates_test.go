// Copyright 2025 Ray Ozzie. All rights reserved.

package pad

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"
)

func TestDecodeIdenticalDuplicates(t *testing.T) {
	ctx := context.Background()
	const chunkBytes = 67
	want := bytes.Repeat([]byte("duplicate collections"), 11)
	for _, copies := range [][2]int{{3, 2}, {5, 3}, {5, 5}} {
		encoder, err := NewPadForEncode(ctx, copies[0], copies[1])
		if err != nil {
			t.Fatal(err)
		}
		frames := encodeBackupChunks(t, encoder, chunkBytes, want)
		for _, legacy := range []bool{false, true} {
			// Duplicate every collection in turn, including ones outside the
			// first K. Reverse input order and vary how many replicas precede it.
			for _, duplicate := range encoder.Collections {
				for _, count := range []int{1, 3, 28} {
					t.Run(fmt.Sprintf("%dof%d/legacy=%t/%s/extra=%d", copies[1], copies[0], legacy, duplicate, count), func(t *testing.T) {
						order := make([]string, count)
						for i := range order {
							order[i] = duplicate
						}
						for i := len(encoder.Collections) - 1; i >= 0; i-- {
							order = append(order, encoder.Collections[i])
						}
						var readers []io.Reader
						for _, name := range order {
							var stream bytes.Buffer
							for _, frame := range frames[name] {
								if legacy {
									frame = replaceBackupID(frame, "")
								}
								stream.Write(frame)
							}
							readers = append(readers, iotest.OneByteReader(bytes.NewReader(stream.Bytes())))
						}
						prepared, err := CheckBackupIDs(readers)
						if err != nil {
							t.Fatal(err)
						}
						decoder, err := NewPadForDecode(ctx, len(readers))
						if err != nil {
							t.Fatal(err)
						}
						var got bytes.Buffer
						if err := decoder.Decode(ctx, prepared, &got); err != nil {
							t.Fatal(err)
						}
						if !bytes.Equal(got.Bytes(), want) {
							t.Fatalf("restored %d bytes, want %d identical bytes", got.Len(), len(want))
						}
					})
				}
			}
		}
	}
}

func TestDecodeConflictingDuplicates(t *testing.T) {
	ctx := context.Background()
	const chunkBytes = 64
	want := bytes.Repeat([]byte{0x37}, 2*chunkBytes+17)
	encoder, err := NewPadForEncode(ctx, 5, 3)
	if err != nil {
		t.Fatal(err)
	}
	frames := encodeBackupChunks(t, encoder, chunkBytes, want)
	for _, legacy := range []bool{false, true} {
		for _, name := range []string{"3A5", "3E5"} {
			for badChunk := 0; badChunk < 3; badChunk++ {
				for _, duplicateFirst := range []bool{false, true} {
					t.Run(fmt.Sprintf("legacy=%t/%s/chunk=%d/duplicateFirst=%t", legacy, name, badChunk+1, duplicateFirst), func(t *testing.T) {
						var readers []io.Reader
						for _, collection := range encoder.Collections {
							var stream bytes.Buffer
							for _, frame := range frames[collection] {
								if legacy {
									frame = replaceBackupID(frame, "")
								}
								stream.Write(frame)
							}
							readers = append(readers, bytes.NewReader(stream.Bytes()))
						}
						var stream bytes.Buffer
						for i, frame := range frames[name] {
							frame = bytes.Clone(frame)
							if legacy {
								frame = replaceBackupID(frame, "")
							}
							if i == badChunk {
								// This changes the last permutation, including data
								// not used by the selected ABC reconstruction.
								frame[len(frame)-1] ^= 1
							}
							stream.Write(frame)
						}
						duplicate := bytes.NewReader(stream.Bytes())
						if duplicateFirst {
							readers = append([]io.Reader{duplicate}, readers...)
						} else {
							readers = append(readers, duplicate)
						}
						decoder, err := NewPadForDecode(ctx, len(readers))
						if err != nil {
							t.Fatal(err)
						}
						var got bytes.Buffer
						err = decoder.Decode(ctx, readers, &got)
						if err == nil || !strings.Contains(err.Error(), "conflicting duplicate") || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), fmt.Sprintf("chunk %d", badChunk+1)) {
							t.Fatalf("expected conflicting duplicate with collection and chunk: %v", err)
						}
						if !bytes.Equal(got.Bytes(), want[:badChunk*chunkBytes]) {
							t.Fatalf("wrote %d bytes; want only the %d-byte agreed prefix", got.Len(), badChunk*chunkBytes)
						}
					})
				}
			}
		}
	}
}

func TestDuplicateCollectionsDoNotMeetThreshold(t *testing.T) {
	ctx := context.Background()
	for _, preflight := range []bool{false, true} {
		t.Run(fmt.Sprintf("preflight=%t", preflight), func(t *testing.T) {
			// Four inputs still provide only two distinct shares of a 3-of-5 backup.
			var readers []io.Reader
			for _, name := range []string{"3A5", "3A5", "3B5", "3B5"} {
				readers = append(readers, bytes.NewReader(chunkWithClaimedSize(name, "1", make([]byte, 6))))
			}
			var err error
			var output bytes.Buffer
			if preflight {
				_, err = CheckBackupIDs(readers)
			} else {
				decoder, createErr := NewPadForDecode(ctx, len(readers))
				if createErr != nil {
					t.Fatal(createErr)
				}
				err = decoder.Decode(ctx, readers, &output)
			}
			if err == nil || !strings.Contains(err.Error(), "not enough distinct collections") || !strings.Contains(err.Error(), "2 < 3") || output.Len() != 0 {
				t.Fatalf("duplicates counted toward threshold: %v; output=%d", err, output.Len())
			}
		})
	}
}

func TestDecodeRejectsIncompleteOrUnreadableDuplicate(t *testing.T) {
	ctx := context.Background()
	const chunkBytes = 67
	want := bytes.Repeat([]byte{0x39}, 2*chunkBytes+17)
	encoder, err := NewPadForEncode(ctx, 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	frames := encodeBackupChunks(t, encoder, chunkBytes, want)
	failure := errors.New("duplicate source failed")
	for _, kind := range []string{"missing_chunk", "short_payload", "read_error", "read_error_with_final_bytes", "trailing_header", "different_backup"} {
		t.Run(kind, func(t *testing.T) {
			var readers []io.Reader
			for _, name := range encoder.Collections {
				readers = append(readers, bytes.NewReader(bytes.Join(frames[name], nil)))
			}
			duplicate := bytes.Join(frames["2C3"][:1], nil)
			var tail io.Reader = bytes.NewReader(nil)
			wantErr, message, prefix := io.ErrUnexpectedEOF, "", chunkBytes
			switch kind {
			case "short_payload", "read_error":
				frame := frames["2C3"][1]
				duplicate = append(duplicate, frame[:len(frame)-1]...)
				if kind == "read_error" {
					tail, wantErr = payloadFailure{failure}, failure
				}
			case "read_error_with_final_bytes":
				frame := frames["2C3"][1]
				headerEnd := 1 + int(frame[0])
				duplicate = append(duplicate, frame[:headerEnd]...)
				tail, wantErr = &duplicateFinalErrorReader{data: bytes.Clone(frame[headerEnd:]), err: failure}, failure
			case "trailing_header":
				duplicate = append(bytes.Join(frames["2C3"], nil), 10, 'x')
				prefix = len(want)
			case "different_backup":
				duplicate = append(duplicate, replaceBackupID(frames["2C3"][1], strings.Repeat("ab", backupIDBytes))...)
				wantErr, message = nil, "different backups"
			}
			readers = append(readers, io.MultiReader(bytes.NewReader(duplicate), tail))
			decoder, err := NewPadForDecode(ctx, len(readers))
			if err != nil {
				t.Fatal(err)
			}
			var got bytes.Buffer
			err = decoder.Decode(ctx, readers, &got)
			if err == nil || (wantErr != nil && !errors.Is(err, wantErr)) || !strings.Contains(err.Error(), message) {
				t.Fatalf("got %v, want %v containing %q", err, wantErr, message)
			}
			if !bytes.Equal(got.Bytes(), want[:prefix]) {
				t.Fatalf("wrote %d bytes, want only %d agreed bytes", got.Len(), prefix)
			}
		})
	}
}

// Return the error alongside the final bytes, exercising Reader's (n > 0, err)
// contract even when those bytes would otherwise complete the duplicate payload.
type duplicateFinalErrorReader struct {
	data []byte
	err  error
}

func (r *duplicateFinalErrorReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	if len(r.data) == 0 {
		return n, r.err
	}
	return n, nil
}

func TestDecodeDuplicateMemory(t *testing.T) {
	ctx := context.Background()
	const payloadSize = 1024 * 1024
	first := chunkWithClaimedSize("2A2", strconv.Itoa(payloadSize), make([]byte, payloadSize))
	second := chunkWithClaimedSize("2B2", strconv.Itoa(payloadSize), make([]byte, payloadSize))
	readers := []io.Reader{bytes.NewReader(first), bytes.NewReader(second)}
	var probes []*payloadReadProbe
	for range 64 {
		probe := &payloadReadProbe{Reader: bytes.NewReader(first)}
		probes = append(probes, probe)
		readers = append(readers, probe)
	}
	decoder, err := NewPadForDecode(ctx, len(readers))
	if err != nil {
		t.Fatal(err)
	}
	// Measure one chunk, with all fixtures and readers already allocated.
	// Re-reading replicas must not allocate another full payload for each one.
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	err = decoder.Decode(ctx, readers, io.Discard)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 32*1024*1024 {
		t.Fatalf("allocated %d bytes comparing 64 replicas of a 1 MiB chunk", allocated)
	}
	for _, probe := range probes {
		if probe.largestRequest > 64*1024 {
			t.Fatalf("duplicate comparison requested a %d-byte buffer", probe.largestRequest)
		}
	}
	t.Logf("allocation for two unique payloads and 64 duplicates: %d bytes", after.TotalAlloc-before.TotalAlloc)
}

type duplicateReadFunc func([]byte) (int, error)

func (f duplicateReadFunc) Read(p []byte) (int, error) { return f(p) }

func TestDecodeDuplicateReadContracts(t *testing.T) {
	const size = 128 * 1024
	for _, kind := range []string{"final_bytes_with_eof", "no_progress", "cancellation"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			data := make([]byte, size)
			var source io.Reader
			var wantErr error
			switch kind {
			case "final_bytes_with_eof":
				source = &duplicateFinalErrorReader{data: data, err: io.EOF}
			case "no_progress":
				calls := 0
				source = duplicateReadFunc(func([]byte) (int, error) {
					calls++
					// Bound the test even if the decoder stops detecting stalls.
					if calls > 200 {
						return 0, io.ErrClosedPipe
					}
					return 0, nil
				})
				wantErr = io.ErrNoProgress
			case "cancellation":
				reader := bytes.NewReader(data)
				source = duplicateReadFunc(func(p []byte) (int, error) {
					n, err := reader.Read(p)
					cancel()
					return n, err
				})
				wantErr = context.Canceled
			}
			readers := []io.Reader{
				bytes.NewReader(chunkWithClaimedSize("2A2", strconv.Itoa(size), data)),
				bytes.NewReader(chunkWithClaimedSize("2B2", strconv.Itoa(size), data)),
				io.MultiReader(bytes.NewReader(chunkWithClaimedSize("2A2", strconv.Itoa(size), nil)), source),
			}
			decoder, err := NewPadForDecode(ctx, len(readers))
			if err != nil {
				t.Fatal(err)
			}
			var got bytes.Buffer
			err = decoder.Decode(ctx, readers, &got)
			if !errors.Is(err, wantErr) {
				t.Fatalf("got %v, want %v", err, wantErr)
			}
			if wantErr == nil {
				if !bytes.Equal(got.Bytes(), data) {
					t.Fatal("matching duplicate changed output")
				}
			} else if got.Len() != 0 {
				t.Fatalf("failed comparison produced %d output bytes", got.Len())
			}
		})
	}
}

func TestDuplicatePreflightValidatesCollectionParameters(t *testing.T) {
	for _, tc := range []struct {
		label, message string
	}{
		{"2A5", "collection parameters mismatch"},
		{"3A4", "collection parameters mismatch"},
		{"3Z5", "invalid collection label"},
		{"3a5", "invalid collection label"},
		{"3A27", "invalid collection label"},
		{"A", "invalid collection label"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			var readers []io.Reader
			for _, name := range []string{"3A5", "3B5", "3C5", tc.label} {
				readers = append(readers, bytes.NewReader(chunkWithClaimedSize(name, "1", nil)))
			}
			prepared, err := CheckBackupIDs(readers)
			if err == nil || prepared != nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("invalid extra input accepted or misreported: %v", err)
			}
		})
	}
}
