// Copyright 2026 Ray Ozzie. All rights reserved.

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"os"
	"sync"
)

// startPadStream runs after path validation and before any output is prepared.
func (s *fileEntropySource) startPadStream(ctx context.Context, skip int64, probeSize int) error {
	if skip > 0 {
		discard := make([]byte, min(skip, 32*1024))
		defer clear(discard)
		for skip > 0 {
			n := min(skip, int64(len(discard)))
			if err := s.Read(ctx, discard[:n]); err != nil {
				return fmt.Errorf("skipping pad stream header: %w", err)
			}
			skip -= n
		}
	}

	// Check signatures immediately, even if the producer sends only a header.
	probe := make([]byte, probeSize)
	prefix := probe[:8]
	if err := s.Read(ctx, prefix); err != nil {
		return fmt.Errorf("reading start of pad stream: %w", err)
	}
	if hasPadFileHeader(prefix) {
		clear(probe)
		return errors.New("pad stream starts with a recognized file or protocol header; supply the generator's raw binary payload")
	}
	r := &padStreamReader{source: s.file}
	s.checked = r
	if err := r.health.check(prefix); err != nil {
		clear(probe)
		return err
	}
	if err := s.Read(ctx, probe[len(prefix):]); err != nil {
		clear(probe)
		return fmt.Errorf("checking start of pad stream: %w", err)
	}
	r.replay = probe
	return nil
}

func hasPadFileHeader(p []byte) bool {
	for _, signature := range [...]string{
		"\x89PNG\r\n\x1a\n", "PK\x03\x04", "PK\x05\x06", "PK\x07\x08", "%PDF-", "\x7fELF",
		"GIF87a", "GIF89a", "HTTP/1.", "-----BEG", `{"data":`, `{"length`,
	} {
		if bytes.HasPrefix(p, []byte(signature)) {
			return true
		}
	}
	// Gzip specifies deflate and requires the high three flag bits to be zero.
	return len(p) >= 4 && bytes.HasPrefix(p, []byte{0x1f, 0x8b, 8}) && p[3]&0xe0 == 0
}

type padStreamReader struct {
	source io.Reader
	mu     sync.Mutex // protects the rolling pad bytes during timeout cleanup
	closed bool
	health padStreamHealth
	replay []byte // Prechecked bytes, already counted in health.
}

func (r *padStreamReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	if len(r.replay) > 0 {
		n := copy(p, r.replay)
		clear(r.replay[:n])
		r.replay = r.replay[n:]
		r.mu.Unlock()
		return n, nil
	}
	r.mu.Unlock()
	n, err := r.source.Read(p)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, os.ErrClosed
	}
	if failure := r.health.check(p[:n]); failure != nil {
		// No bytes from this read may be used after a health failure.
		return 0, failure
	}
	return n, err
}

func (r *padStreamReader) clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.health = padStreamHealth{}
	clear(r.replay)
	r.replay = nil
}

// Cutoffs assume independent uniform pad bytes. Per byte position, a run of
// 13 has probability 2^-96. For a window, bound frequency failures by 256
// binomial(512, 1/256) upper tails and bit imbalance by the two tails of
// binomial(4096, 1/2). The eight bit positions add 16 binomial(512, 1/2)
// lower tails at 120. Summing across 2^40 positions bounds rolling false
// alarms below 2e-17 per TiB, without assuming independent windows.
const (
	padWindow       = 512
	runCutoff       = 13
	frequencyCutoff = 36
	bitTailCutoff   = 1680
	positionCutoff  = 120
)

type padStreamHealth struct {
	window       [padWindow]byte
	counts       [256]uint16
	next         int
	size         int
	ones         int
	positionOnes [8]int
	last         byte
	run          int
}

func (h *padStreamHealth) check(p []byte) error {
	for _, b := range p {
		if b != h.last {
			h.run = 0
			h.last = b
		}
		h.run++
		if h.run >= runCutoff {
			return errors.New("pad stream health check failed: repeated byte run")
		}

		var old byte
		if h.size == padWindow {
			old = h.window[h.next]
			h.counts[old]--
			h.ones -= bits.OnesCount8(old)
		} else {
			h.size++
		}
		h.window[h.next] = b
		h.next = (h.next + 1) % padWindow
		h.counts[b]++
		h.ones += bits.OnesCount8(b)
		if h.counts[b] >= frequencyCutoff {
			return errors.New("pad stream health check failed: excessive byte frequency")
		}
		if h.size == padWindow && (h.ones <= bitTailCutoff || h.ones >= padWindow*8-bitTailCutoff) {
			return errors.New("pad stream health check failed: bit imbalance")
		}
		for bit := range h.positionOnes {
			h.positionOnes[bit] += int((b>>bit)&1) - int((old>>bit)&1)
			count := h.positionOnes[bit]
			if h.size == padWindow && (count <= positionCutoff || count >= padWindow-positionCutoff) {
				return errors.New("pad stream health check failed: individual bit position imbalance")
			}
		}
	}
	return nil
}
