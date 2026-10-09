// Copyright 2025 Ray Ozzie. All rights reserved.

package pad

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
)

func encodeBackupChunks(t *testing.T, encoder *Pad, chunkBytes int, data []byte) map[string][][]byte {
	t.Helper()
	buffers := make(map[string][]*bytes.Buffer)
	err := encoder.Encode(context.Background(), chunkBytes*encoder.PermutationCount, bytes.NewReader(data), NewTestRNG(0x37),
		func(name string, _ int, _ string) (io.WriteCloser, error) {
			chunk := new(bytes.Buffer)
			buffers[name] = append(buffers[name], chunk)
			return &nopCloser{chunk}, nil
		}, "bin")
	if err != nil {
		t.Fatal(err)
	}
	chunks := make(map[string][][]byte)
	for name, frames := range buffers {
		for _, frame := range frames {
			chunks[name] = append(chunks[name], bytes.Clone(frame.Bytes()))
		}
	}
	return chunks
}

func replaceBackupID(frame []byte, id string) []byte {
	length := int(frame[0])
	fields := strings.Split(string(frame[1:1+length]), ":")
	fields = fields[:3]
	if id != "" {
		fields = append(fields, id)
	}
	header := strings.Join(fields, ":")
	result := append([]byte{byte(len(header))}, header...)
	return append(result, frame[1+length:]...)
}

func TestBackupIdentifierPerEncodeRun(t *testing.T) {
	encoder, err := NewPadForEncode(context.Background(), 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	var previous string
	for range 2 {
		// Reusing both the Pad and the same deterministic pad input must still
		// identify a new backup. Every chunk of that run must share its ID.
		chunks := encodeBackupChunks(t, encoder, 16, bytes.Repeat([]byte("same input"), 10))
		var id string
		for _, frames := range chunks {
			for _, frame := range frames {
				_, _, _, got, err := extractFromChunkName(string(frame[1 : 1+int(frame[0])]))
				if err != nil || len(got) != 2*backupIDBytes {
					t.Fatalf("new chunk lacks a valid backup identifier: %q, %v", got, err)
				}
				if id == "" {
					id = got
				} else if got != id {
					t.Fatal("one encode run produced different backup identifiers")
				}
			}
		}
		if id == previous {
			t.Fatal("a later Encode call reused the backup identifier")
		}
		previous = id
	}
}

func TestDecodeRejectsMixedBackups(t *testing.T) {
	ctx := context.Background()
	const chunkBytes = 64
	data := bytes.Repeat([]byte{0x35}, 2*chunkBytes+17)
	for _, copies := range [][2]int{{3, 2}, {5, 3}} {
		encoder, err := NewPadForEncode(ctx, copies[0], copies[1])
		if err != nil {
			t.Fatal(err)
		}
		first := encodeBackupChunks(t, encoder, chunkBytes, data)
		second := encodeBackupChunks(t, encoder, chunkBytes, data)
		for _, wrongCollection := range append(append([]string{}, encoder.Collections...), "all") {
			for wrongChunk := 0; wrongChunk < 3; wrongChunk++ {
				t.Run(fmt.Sprintf("%dof%d/%s/chunk%d", copies[1], copies[0], wrongCollection, wrongChunk+1), func(t *testing.T) {
					var readers []io.Reader
					for _, name := range encoder.Collections {
						var stream bytes.Buffer
						for i, frame := range first[name] {
							if i == wrongChunk && (name == wrongCollection || wrongCollection == "all") {
								frame = second[name][i]
							}
							stream.Write(frame)
						}
						readers = append(readers, bytes.NewReader(stream.Bytes()))
					}
					// All chunks from a single other backup are valid as a set.
					validChunks := wrongChunk
					if wrongCollection == "all" && wrongChunk == 0 {
						// Here only the first round is changed, so the second round
						// must fail against the first round's identifier.
						validChunks = 1
					}
					decoder, err := NewPadForDecode(ctx, len(readers))
					if err != nil {
						t.Fatal(err)
					}
					var restored bytes.Buffer
					err = decoder.Decode(ctx, readers, &restored)
					if err == nil || !strings.Contains(err.Error(), "different backups") {
						t.Fatalf("accepted mixed backup chunks or returned wrong error: %v", err)
					}
					if !bytes.Equal(restored.Bytes(), data[:validChunks*chunkBytes]) {
						t.Fatalf("wrote %d bytes; want only the %d-byte valid prefix", restored.Len(), validChunks*chunkBytes)
					}
				})
			}
		}
	}
}

func TestBackupPreflightAndLegacyDecoding(t *testing.T) {
	ctx := context.Background()
	encoder, err := NewPadForEncode(ctx, 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("restore"), 30)
	chunks := encodeBackupChunks(t, encoder, 64, data)
	for _, kind := range []string{"new", "legacy", "mixed_first", "mixed_extra", "legacy_later", "different_extra", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			var readers []io.Reader
			for collection, name := range encoder.Collections {
				var stream bytes.Buffer
				for index, frame := range chunks[name] {
					if kind == "legacy" || (kind == "mixed_first" && collection == 0) || (kind == "mixed_extra" && collection == 2) || (kind == "legacy_later" && index == 1) {
						frame = replaceBackupID(frame, "")
					}
					if kind == "different_extra" && collection == 2 {
						frame = replaceBackupID(frame, strings.Repeat("ab", backupIDBytes))
					}
					if kind == "malformed" && collection == 1 {
						frame = replaceBackupID(frame, "not-an-id")
					}
					stream.Write(frame)
				}
				readers = append(readers, bytes.NewReader(stream.Bytes()))
			}
			prepared, err := CheckBackupIDs(readers)
			if kind != "new" && kind != "legacy" && kind != "legacy_later" {
				if err == nil || prepared != nil {
					t.Fatalf("preflight accepted %s: %v", kind, err)
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
			err = decoder.Decode(ctx, prepared, &restored)
			if kind == "legacy_later" {
				if err == nil || !strings.Contains(err.Error(), "different backups") || !bytes.Equal(restored.Bytes(), data[:64]) {
					t.Fatalf("removed identifiers in a later round bypassed the check: %v", err)
				}
				return
			}
			if err != nil || !bytes.Equal(restored.Bytes(), data) {
				t.Fatalf("preflight consumed data or broke %s restore: %v", kind, err)
			}
		})
	}
}

func TestBackupPreflightRejectsMalformedHeaders(t *testing.T) {
	for _, header := range []string{
		"", "2A2:1:64:", "2A2:1:64:abc", "2A2:1:64:" + strings.Repeat("z", 32),
		"2A2:1:64:" + strings.Repeat("ab", 17), "2A2:1:64:" + strings.Repeat("ab", 16) + ":extra",
	} {
		t.Run(header, func(t *testing.T) {
			frame := append([]byte{byte(len(header))}, header...)
			if _, err := CheckBackupIDs([]io.Reader{bytes.NewReader(frame)}); err == nil {
				t.Fatal("malformed header accepted")
			}
		})
	}
	for _, frame := range [][]byte{nil, {12}, {12, '2', 'A', '2'}} {
		if _, err := CheckBackupIDs([]io.Reader{bytes.NewReader(frame)}); err == nil {
			t.Fatal("missing or truncated header accepted")
		}
	}
	if _, err := CheckBackupIDs(nil); err == nil {
		t.Fatal("empty collection set accepted")
	}
}
