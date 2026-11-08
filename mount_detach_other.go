//go:build !linux
// +build !linux

package main

import (
	"fmt"
	"os/exec"
)

func configureDetachedProcess(command *exec.Cmd) error {
	return fmt.Errorf("background mount mode is currently supported only on Linux")
}
