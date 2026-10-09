// Copyright 2025 Ray Ozzie. All rights reserved.

//go:build unix

package main

import (
	"errors"
	"os"
	"syscall"
)

// Nonblocking open prevents a FIFO with no producer from hanging in open(2).
// Some files (notably Darwin FIFOs) cannot use Go's runtime poller. Read retries
// their would-block errors without changing this descriptor to blocking mode.
func openEntropyFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}

func entropyReadWouldBlock(err error) bool {
	return errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK)
}
