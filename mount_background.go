package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	backgroundChildEnvironment = "DUPLICACY_FUSE_BACKGROUND_CHILD"
	backgroundReadyEnvironment = "DUPLICACY_FUSE_READY_FD"
)

func startBackgroundMount(args []string, mountpoint string, stderr io.Writer) error {
	logFile, err := os.CreateTemp("", "duplicacy-fuse-mount-*.log")
	if err != nil {
		return fmt.Errorf("create background mount log: %w", err)
	}
	logPath := logFile.Name()

	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		logFile.Close()
		os.Remove(logPath)
		return fmt.Errorf("create mount readiness pipe: %w", err)
	}

	childArgs := append([]string{"mount"}, args...)
	cmd := exec.Command(os.Args[0], childArgs...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.ExtraFiles = []*os.File{readyWriter}
	cmd.Env = replaceEnvironment(os.Environ(), map[string]string{
		backgroundChildEnvironment: "1",
		backgroundReadyEnvironment: "3",
	})
	if err := configureDetachedProcess(cmd); err != nil {
		readyReader.Close()
		readyWriter.Close()
		logFile.Close()
		os.Remove(logPath)
		return err
	}
	if err := cmd.Start(); err != nil {
		readyReader.Close()
		readyWriter.Close()
		logFile.Close()
		os.Remove(logPath)
		return fmt.Errorf("start background mount: %w", err)
	}
	_ = readyWriter.Close()
	_ = logFile.Close()
	defer readyReader.Close()

	ready := make(chan error, 1)
	go func() {
		var signal [1]byte
		_, err := io.ReadFull(readyReader, signal[:])
		ready <- err
	}()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	timer := time.NewTimer(60 * time.Second)
	defer timer.Stop()

	select {
	case err := <-ready:
		if err != nil {
			_ = cmd.Process.Kill()
			childErr := <-exited
			return fmt.Errorf("background mount exited before becoming ready (%v, %v); log: %s", err, childErr, logPath)
		}
		fmt.Fprintf(stderr, "Mounted %s in background. Log: %s\n", mountpoint, logPath)
		return nil
	case childErr := <-exited:
		return fmt.Errorf("background mount exited before becoming ready (%v); log: %s", childErr, logPath)
	case <-timer.C:
		_ = cmd.Process.Kill()
		childErr := <-exited
		return fmt.Errorf("background mount did not become ready within 60 seconds (%v); log: %s", childErr, logPath)
	}
}

func backgroundReadyNotifier() (func(), error) {
	value := os.Getenv(backgroundReadyEnvironment)
	if value == "" {
		return nil, nil
	}
	fd, err := strconv.Atoi(value)
	if err != nil || fd < 3 {
		return nil, fmt.Errorf("invalid background readiness file descriptor")
	}
	file := os.NewFile(uintptr(fd), "duplicacy-fuse-ready")
	if file == nil {
		return nil, fmt.Errorf("open background readiness file descriptor")
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			if _, err := file.Write([]byte{1}); err != nil {
				fmt.Fprintf(os.Stderr, "signal background mount readiness: %v\n", err)
			}
			_ = file.Close()
		})
	}, nil
}

func replaceEnvironment(environment []string, values map[string]string) []string {
	result := make([]string, 0, len(environment)+len(values))
	for _, item := range environment {
		key := strings.SplitN(item, "=", 2)[0]
		if _, replace := values[key]; !replace {
			result = append(result, item)
		}
	}
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}
