//go:build !linux

package main

import (
	"encoding/json"
	"errors"
)

const maxGUIControlMessage = 4 * 1024 * 1024

type guiIPCMessage struct {
	Type        string
	Message     string
	Attempt     int
	Value       string
	Revisions   json.RawMessage
	SnapshotIDs json.RawMessage
	Entries     json.RawMessage
}

type guiMountControl struct{}

func newGUIMountControl(int) (*guiMountControl, error) {
	return nil, errors.New("GNOME mount helper is supported only on Linux")
}

func (*guiMountControl) send(guiIPCMessage) error { return errors.New("unsupported platform") }
func (*guiMountControl) requestPassword(int, error) (string, error) {
	return "", errors.New("unsupported platform")
}
func (*guiMountControl) watchMount(string)        {}
func (*guiMountControl) isUnmountRequested() bool { return false }
func (*guiMountControl) notifyReadError()         {}
func (*guiMountControl) Close() error             { return nil }
