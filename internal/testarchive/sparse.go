// Copyright 2025 Ray Ozzie. All rights reserved.

// Package testarchive builds hostile archive fixtures for regression tests.
// It is not used by the Padlock executable.
package testarchive

import (
	"archive/tar"
	"bytes"
	"encoding/binary"
	"fmt"
	"strconv"
)

// Sparse returns a tiny archive whose sole entry expands to logicalSize zero
// bytes. Build the headers directly: Go's tar.Writer does not emit sparse files.
// Supported versions are "gnu", "pax0.0", "pax0.1", and "pax1.0".
func Sparse(name string, logicalSize int64, version string) []byte {
	var archive bytes.Buffer
	if version == "gnu" {
		header := rawHeader(name, 0, tar.TypeGNUSparse)
		copy(header[257:265], "ustar  \x00")
		putNumber(header[483:495], logicalSize)
		checksum(header)
		archive.Write(header)
	} else {
		var records, body bytes.Buffer
		switch version {
		case "pax0.0", "pax0.1":
			records.WriteString(paxRecord("GNU.sparse.size", strconv.FormatInt(logicalSize, 10)))
			records.WriteString(paxRecord("GNU.sparse.numblocks", "1"))
			if version == "pax0.0" {
				records.WriteString(paxRecord("GNU.sparse.offset", "0"))
				records.WriteString(paxRecord("GNU.sparse.numbytes", "0"))
			} else {
				records.WriteString(paxRecord("GNU.sparse.map", "0,0"))
			}
		case "pax1.0":
			records.WriteString(paxRecord("GNU.sparse.major", "1"))
			records.WriteString(paxRecord("GNU.sparse.minor", "0"))
			records.WriteString(paxRecord("GNU.sparse.name", name))
			records.WriteString(paxRecord("GNU.sparse.realsize", strconv.FormatInt(logicalSize, 10)))
			body.WriteString("0\n") // Sparse map with no data extents.
			body.Write(make([]byte, 510))
		default:
			panic("unknown sparse fixture version: " + version)
		}
		archive.Write(rawHeader("PaxHeaders.0/sparse", int64(records.Len()), tar.TypeXHeader))
		archive.Write(records.Bytes())
		archive.Write(make([]byte, (512-records.Len()%512)%512))
		archive.Write(rawHeader(name, int64(body.Len()), tar.TypeReg))
		archive.Write(body.Bytes())
	}
	archive.Write(make([]byte, 1024))
	return archive.Bytes()
}

func rawHeader(name string, size int64, kind byte) []byte {
	header := make([]byte, 512)
	copy(header[:100], name)
	putNumber(header[100:108], 0600)
	putNumber(header[108:116], 0)
	putNumber(header[116:124], 0)
	putNumber(header[124:136], size)
	putNumber(header[136:148], 0)
	header[156] = kind
	copy(header[257:265], "ustar\x0000")
	checksum(header)
	return header
}

func putNumber(field []byte, n int64) {
	value := fmt.Sprintf("%0*o", len(field)-1, n)
	if len(value) < len(field) {
		copy(field, value)
		field[len(field)-1] = 0
		return
	}
	// GNU uses base-256 for values that do not fit an octal header field.
	clear(field)
	binary.BigEndian.PutUint64(field[len(field)-8:], uint64(n))
	field[0] = 0x80
}

func checksum(header []byte) {
	copy(header[148:156], "        ")
	var sum int
	for _, b := range header {
		sum += int(b)
	}
	copy(header[148:156], fmt.Sprintf("%06o\x00 ", sum))
}

func paxRecord(key, value string) string {
	body := key + "=" + value + "\n"
	size := len(body) + 2
	for {
		record := fmt.Sprintf("%d %s", size, body)
		if len(record) == size {
			return record
		}
		size = len(record)
	}
}
