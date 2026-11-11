//go:build linux

package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestGUIControlRequestsPasswordOnlyOverPrivateSocket(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	clientFile := os.NewFile(uintptr(fds[0]), "client")
	serverFile := os.NewFile(uintptr(fds[1]), "server")
	serverConnection, err := net.FileConn(serverFile)
	_ = serverFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer serverConnection.Close()
	control, err := newGUIMountControl(int(clientFile.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()

	serverErr := make(chan error, 1)
	go func() {
		reader := bufio.NewScanner(serverConnection)
		if !reader.Scan() {
			serverErr <- reader.Err()
			return
		}
		var request guiIPCMessage
		if err := json.Unmarshal(reader.Bytes(), &request); err != nil {
			serverErr <- err
			return
		}
		if request.Type != "password-required" || strings.Contains(string(reader.Bytes()), "secret") {
			serverErr <- errUnexpectedGUIRequest
			return
		}
		_, err := serverConnection.Write([]byte(`{"type":"password","value":"secret"}` + "\n"))
		serverErr <- err
	}()

	password, err := control.requestPassword(1, nil)
	if err != nil || password != "secret" {
		t.Fatalf("password request = %q, %v", password, err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestGUIRevisionPayloadRemainsJSON(t *testing.T) {
	message, err := json.Marshal(guiIPCMessage{
		Type:      "revisions-ready",
		Revisions: json.RawMessage(`[ {"revision": 7, "created_at": 1234} ]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Type      string          `json:"type"`
		Revisions json.RawMessage `json:"revisions"`
	}
	if err := json.Unmarshal(message, &decoded); err != nil {
		t.Fatal(err)
	}
	var revisions []struct {
		Revision  int   `json:"revision"`
		CreatedAt int64 `json:"created_at"`
	}
	if decoded.Type != "revisions-ready" || json.Unmarshal(decoded.Revisions, &revisions) != nil {
		t.Fatalf("invalid revision IPC message: %s", message)
	}
	if len(revisions) != 1 || revisions[0].Revision != 7 || revisions[0].CreatedAt != 1234 {
		t.Fatalf("decoded revisions = %#v", revisions)
	}
}

func TestGUISnapshotIDPayloadRemainsJSON(t *testing.T) {
	message, err := json.Marshal(guiIPCMessage{
		Type:        "snapshots-ready",
		SnapshotIDs: json.RawMessage(`["workstation", "laptop"]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Type        string          `json:"type"`
		SnapshotIDs json.RawMessage `json:"snapshot_ids"`
	}
	if err := json.Unmarshal(message, &decoded); err != nil {
		t.Fatal(err)
	}
	var snapshotIDs []string
	if decoded.Type != "snapshots-ready" || json.Unmarshal(decoded.SnapshotIDs, &snapshotIDs) != nil {
		t.Fatalf("invalid snapshot ID IPC message: %s", message)
	}
	if len(snapshotIDs) != 2 || snapshotIDs[0] != "workstation" || snapshotIDs[1] != "laptop" {
		t.Fatalf("decoded snapshot IDs = %#v", snapshotIDs)
	}
}

func TestValidateGUIMountpoint(t *testing.T) {
	mountpoint := t.TempDir()
	if err := validateGUIMountpoint(mountpoint); err != nil {
		t.Fatalf("valid empty mountpoint: %v", err)
	}
	if err := os.WriteFile(mountpoint+"/occupied", []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateGUIMountpoint(mountpoint); err == nil {
		t.Fatal("non-empty mountpoint was accepted")
	}
	link := mountpoint + "-link"
	if err := os.Symlink(mountpoint, link); err != nil {
		t.Fatal(err)
	}
	if err := validateGUIMountpoint(link); err == nil {
		t.Fatal("mountpoint symlink was accepted")
	}
}

var errUnexpectedGUIRequest = &unexpectedGUIRequest{}

type unexpectedGUIRequest struct{}

func (*unexpectedGUIRequest) Error() string { return "unexpected GUI IPC request" }
