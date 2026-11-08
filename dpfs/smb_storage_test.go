package dpfs

import (
	"errors"
	"net"
	"os"
	"testing"

	duplicacy "github.com/gilbertchen/duplicacy/src"
	"github.com/hirochachacha/go-smb2"
)

type reconnectTestStorage struct {
	duplicacy.Storage
	findErr       error
	findCalls     int
	downloadErr   error
	downloadData  []byte
	downloadPart  []byte
	downloadCalls int
}

func (storage *reconnectTestStorage) FindChunk(threadIndex int, chunkID string, isFossil bool) (string, bool, int64, error) {
	storage.findCalls++
	if storage.findErr != nil {
		return "", false, 0, storage.findErr
	}
	return "chunks/fixture", true, 7, nil
}

func (storage *reconnectTestStorage) DownloadFile(threadIndex int, filePath string, chunk *duplicacy.Chunk) error {
	storage.downloadCalls++
	if len(storage.downloadPart) > 0 {
		_, _ = chunk.Write(storage.downloadPart)
	}
	if storage.downloadErr != nil {
		return storage.downloadErr
	}
	_, err := chunk.Write(storage.downloadData)
	return err
}

func TestSMBReconnectRetriesFindChunkOnce(t *testing.T) {
	original := &reconnectTestStorage{findErr: &smb2.TransportError{Err: net.ErrClosed}}
	replacement := &reconnectTestStorage{}
	reopenCalls := 0
	storage := &smbReconnectStorage{
		Storage: original,
		reopen: func() (duplicacy.Storage, error) {
			reopenCalls++
			return replacement, nil
		},
	}

	path, exists, size, err := storage.FindChunk(0, "fixture", false)
	if err != nil || path != "chunks/fixture" || !exists || size != 7 {
		t.Fatalf("FindChunk() = %q, %t, %d, %v", path, exists, size, err)
	}
	if original.findCalls != 1 || replacement.findCalls != 1 || reopenCalls != 1 {
		t.Fatalf("calls = original:%d replacement:%d reopen:%d", original.findCalls, replacement.findCalls, reopenCalls)
	}
}

func TestSMBReconnectRetriesDownloadWithoutAppendingPartialData(t *testing.T) {
	original := &reconnectTestStorage{downloadErr: &smb2.TransportError{Err: net.ErrClosed}}
	replacement := &reconnectTestStorage{downloadData: []byte("complete chunk")}
	storage := &smbReconnectStorage{
		Storage: original,
		reopen: func() (duplicacy.Storage, error) {
			return replacement, nil
		},
	}
	chunk := duplicacy.CreateChunk(&duplicacy.Config{}, true)
	chunk.Reset(false)

	if err := storage.DownloadFile(0, "chunks/fixture", chunk); err != nil {
		t.Fatal(err)
	}
	if got := string(chunk.GetBytes()); got != "complete chunk" {
		t.Fatalf("downloaded data = %q", got)
	}
	if original.downloadCalls != 1 || replacement.downloadCalls != 1 {
		t.Fatalf("download calls = original:%d replacement:%d", original.downloadCalls, replacement.downloadCalls)
	}
}

func TestSMBReconnectDoesNotRetryPartialOrNonTransientReads(t *testing.T) {
	transient := &smb2.TransportError{Err: net.ErrClosed}
	original := &reconnectTestStorage{downloadErr: transient, downloadPart: []byte("partial")}
	reopenCalls := 0
	storage := &smbReconnectStorage{
		Storage: original,
		reopen: func() (duplicacy.Storage, error) {
			reopenCalls++
			return &reconnectTestStorage{}, nil
		},
	}
	chunk := duplicacy.CreateChunk(&duplicacy.Config{}, true)
	chunk.Reset(false)

	if err := storage.DownloadFile(0, "chunks/fixture", chunk); !errors.Is(err, transient) {
		t.Fatalf("partial download error = %v, want original transport error", err)
	}
	if reopenCalls != 0 || original.downloadCalls != 1 {
		t.Fatalf("partial transfer retried: reopen=%d download=%d", reopenCalls, original.downloadCalls)
	}

	original.findErr = os.ErrPermission
	if _, _, _, err := storage.FindChunk(0, "fixture", false); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("permission error = %v", err)
	}
	if reopenCalls != 0 {
		t.Fatalf("non-transient error triggered %d reconnects", reopenCalls)
	}
}

func TestSMBReconnectRecognizesExpiredSessionsOnly(t *testing.T) {
	if !isRetryableSMBReadError(&smb2.ResponseError{Code: statusNetworkSessionExpired}) {
		t.Fatal("expired SMB session was not marked retryable")
	}
	if !isRetryableSMBReadError(&smb2.ResponseError{Code: statusInvalidHandle}) {
		t.Fatal("invalid SMB handle was not marked retryable")
	}
	if isRetryableSMBReadError(&smb2.ResponseError{Code: 0xC0000022}) {
		t.Fatal("access denied was marked retryable")
	}
}
