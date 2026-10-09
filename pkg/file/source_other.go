// Copyright 2025 Ray Ozzie. All rights reserved.

//go:build !unix

package file

import "os"

func openSourceFile(path string) (*os.File, error) {
	return os.Open(path)
}
