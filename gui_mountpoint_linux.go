//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func validateGUIMountpoint(mountpoint string) error {
	absolute, err := filepath.Abs(mountpoint)
	if err != nil {
		return fmt.Errorf("resolve mountpoint: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return fmt.Errorf("inspect mountpoint: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("GUI mountpoint must be a real directory, not a symlink")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("GUI mountpoint must be owned by the current user")
	}
	if info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("GUI mountpoint must not be group- or world-writable")
	}
	entries, err := os.ReadDir(absolute)
	if err != nil {
		return fmt.Errorf("inspect mountpoint contents: %w", err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("GUI mountpoint must be empty")
	}
	return nil
}
