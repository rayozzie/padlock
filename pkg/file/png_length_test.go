// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestExtractDataFromPNGChunkBounds(t *testing.T) {
	payload := []byte("padlock chunk payload")
	var encoded bytes.Buffer
	if err := encodePNGWithData(&encoded, createSmallPNG(), payload); err != nil {
		t.Fatal(err)
	}
	original := encoded.Bytes()
	typePos := bytes.Index(original, []byte("rAWd"))
	if typePos < 4 {
		t.Fatal("encoded PNG has no rAWd chunk")
	}
	dataStart := typePos + 4
	dataEnd := dataStart + len(payload)

	for _, tc := range []struct {
		name    string
		length  uint32
		end     int
		message string
	}{
		{"valid", uint32(len(payload)), len(original), ""},
		{"payload_truncated", uint32(len(payload)), dataEnd - 1, "invalid PNG chunk length"},
		{"crc_missing", uint32(len(payload)), dataEnd, "no CRC found"},
		{"crc_one_byte", uint32(len(payload)), dataEnd + 1, "no CRC found"},
		{"crc_two_bytes", uint32(len(payload)), dataEnd + 2, "no CRC found"},
		{"crc_three_bytes", uint32(len(payload)), dataEnd + 3, "no CRC found"},
		{"length_exceeds_file", uint32(len(original)-dataStart) + 1, len(original), "invalid PNG chunk length"},
		// Exercise both signed addition overflow and uint32-to-int overflow
		// on 32-bit builds, using small files rather than huge allocations.
		{"int32_end_overflow", 0x80000000 - uint32(dataStart), len(original), "invalid PNG chunk length"},
		{"max_int32", 0x7fffffff, len(original), "invalid PNG chunk length"},
		{"high_bit_set", 0x80000000, len(original), "invalid PNG chunk length"},
		{"max_uint32_minus_one", 0xfffffffe, len(original), "invalid PNG chunk length"},
		{"max_uint32", 0xffffffff, len(original), "invalid PNG chunk length"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := bytes.Clone(original[:tc.end])
			binary.BigEndian.PutUint32(input[typePos-4:typePos], tc.length)
			got, err := ExtractDataFromPNG(bytes.NewReader(input))
			if tc.message != "" {
				if err == nil || !strings.Contains(err.Error(), tc.message) || got != nil {
					t.Fatalf("expected %q and no payload, got %x, %v", tc.message, got, err)
				}
				return
			}
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("valid PNG payload changed: got %x, error %v", got, err)
			}
		})
	}
}

func TestExtractDataFromPNGEmptyChunkCRC(t *testing.T) {
	var encoded bytes.Buffer
	if err := encodePNGWithData(&encoded, createSmallPNG(), nil); err != nil {
		t.Fatal(err)
	}
	original := encoded.Bytes()
	typePos := bytes.Index(original, []byte("rAWd"))
	if typePos < 4 {
		t.Fatal("encoded PNG has no rAWd chunk")
	}
	if got, err := ExtractDataFromPNG(bytes.NewReader(original)); err != nil || len(got) != 0 {
		t.Fatalf("empty chunk was not accepted: %x, %v", got, err)
	}
	for crcBytes := 0; crcBytes < 4; crcBytes++ {
		if got, err := ExtractDataFromPNG(bytes.NewReader(original[:typePos+4+crcBytes])); err == nil || !strings.Contains(err.Error(), "no CRC found") || got != nil {
			t.Fatalf("accepted empty chunk with %d checksum bytes: %x, %v", crcBytes, got, err)
		}
	}
	corrupted := bytes.Clone(original)
	corrupted[typePos+4] ^= 1
	if got, err := ExtractDataFromPNG(bytes.NewReader(corrupted)); err == nil || !strings.Contains(err.Error(), "CRC mismatch") || got != nil {
		t.Fatalf("accepted empty chunk with a bad checksum: %x, %v", got, err)
	}
}
