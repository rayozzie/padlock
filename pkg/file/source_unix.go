// Copyright 2025 Ray Ozzie. All rights reserved.

//go:build unix

package file

import (
	"os"
	"syscall"
)

func openSourceFile(path string) (*os.File, error) {
	// A regular file may have been replaced since Walk/Lstat. Do not block on
	// a FIFO or follow a newly substituted leaf symlink before checking f.Stat.
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}
