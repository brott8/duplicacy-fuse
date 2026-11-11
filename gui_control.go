//go:build linux

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
)

const maxGUIControlMessage = 4 * 1024 * 1024

type guiIPCMessage struct {
	Type        string          `json:"type"`
	Message     string          `json:"message,omitempty"`
	Attempt     int             `json:"attempt,omitempty"`
	Value       string          `json:"value,omitempty"`
	Revisions   json.RawMessage `json:"revisions,omitempty"`
	SnapshotIDs json.RawMessage `json:"snapshot_ids,omitempty"`
}

type guiMountControl struct {
	connection       *net.UnixConn
	reader           *bufio.Scanner
	writeMu          sync.Mutex
	unmountRequested atomic.Bool
	readErrorSent    atomic.Bool
}

func newGUIMountControl(fd int) (*guiMountControl, error) {
	file := os.NewFile(uintptr(fd), "duplicacy-fuse-gui-control")
	if file == nil {
		return nil, errors.New("invalid GUI control descriptor")
	}
	connection, err := net.FileConn(file)
	_ = file.Close()
	if err != nil {
		return nil, fmt.Errorf("open GUI control socket: %w", err)
	}
	unix, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		return nil, errors.New("GUI control descriptor is not a Unix socket")
	}
	reader := bufio.NewScanner(unix)
	reader.Buffer(make([]byte, 1024), maxGUIControlMessage)
	return &guiMountControl{connection: unix, reader: reader}, nil
}

func (control *guiMountControl) send(message guiIPCMessage) error {
	encoded, err := json.Marshal(message)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	control.writeMu.Lock()
	defer control.writeMu.Unlock()
	for len(encoded) > 0 {
		written, err := control.connection.Write(encoded)
		if err != nil {
			return err
		}
		if written == 0 {
			return errors.New("short write on GUI control socket")
		}
		encoded = encoded[written:]
	}
	return nil
}

func (control *guiMountControl) requestPassword(attempt int, _ error) (string, error) {
	message := guiIPCMessage{Type: "password-required", Attempt: attempt}
	if attempt > 1 {
		message.Message = "The password was not accepted or the storage could not be read."
	}
	if err := control.send(message); err != nil {
		return "", fmt.Errorf("request storage password: %w", err)
	}
	if !control.reader.Scan() {
		if err := control.reader.Err(); err != nil {
			return "", fmt.Errorf("read GUI password response: %w", err)
		}
		return "", errors.New("GUI password request was closed")
	}
	var response guiIPCMessage
	if err := json.Unmarshal(control.reader.Bytes(), &response); err != nil {
		return "", fmt.Errorf("decode GUI password response: %w", err)
	}
	if response.Type == "cancel" {
		return "", errors.New("password request cancelled")
	}
	if response.Type != "password" || len(response.Value) == 0 || len(response.Value) > 4096 {
		response.Value = ""
		return "", errors.New("invalid GUI password response")
	}
	password := response.Value
	response.Value = ""
	return password, nil
}

func (control *guiMountControl) watchMount(mountpoint string) {
	for control.reader.Scan() {
		var message guiIPCMessage
		if err := json.Unmarshal(control.reader.Bytes(), &message); err == nil && message.Type == "unmount" {
			control.unmountRequested.Store(true)
			if err := unmountGUIMount(filepath.Clean(mountpoint)); err != nil {
				_ = control.send(guiIPCMessage{Type: "unmount-failed", Message: err.Error()})
				continue
			}
			return
		}
	}
	control.unmountRequested.Store(true)
	if err := unmountGUIMount(filepath.Clean(mountpoint)); err != nil {
		os.Exit(1)
	}
}

func (control *guiMountControl) isUnmountRequested() bool {
	return control.unmountRequested.Load()
}

func (control *guiMountControl) notifyReadError() {
	if control.readErrorSent.CompareAndSwap(false, true) {
		_ = control.send(guiIPCMessage{Type: "read-error"})
	}
}

func unmountGUIMount(mountpoint string) error {
	var lastErr error
	for _, command := range []string{"fusermount", "fusermount3"} {
		if _, err := exec.LookPath(command); err != nil {
			lastErr = err
			continue
		}
		if err := exec.Command(command, "-u", mountpoint).Run(); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return fmt.Errorf("unmount FUSE mount: %w", lastErr)
}

func (control *guiMountControl) Close() error {
	return control.connection.Close()
}
