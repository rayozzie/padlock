// Copyright 2025 Ray Ozzie. All rights reserved.

//go:build !unix

package main

import "os"

func openEntropyFile(path string) (*os.File, error) {
	return os.Open(path)
}

func entropyReadWouldBlock(error) bool { return false }
