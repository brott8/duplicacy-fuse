//go:build !linux

package main

import "fmt"

func validateGUIMountpoint(string) error {
	return fmt.Errorf("GNOME mount helper is supported only on Linux")
}
