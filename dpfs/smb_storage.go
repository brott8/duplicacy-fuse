package dpfs

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	duplicacy "github.com/gilbertchen/duplicacy/src"
	"github.com/hirochachacha/go-smb2"
)

// SMB NTSTATUS values that indicate a transport, session, or handle that can be reopened.
const (
	statusInvalidHandle          = 0xC0000008
	statusIOTimeout              = 0xC00000B5
	statusNetworkNameDeleted     = 0xC00000C9
	statusFileClosed             = 0xC0000128
	statusPipeBroken             = 0xC000014B
	statusUserSessionDeleted     = 0xC0000203
	statusConnectionDisconnected = 0xC000020C
	statusConnectionReset        = 0xC000020D
	statusConnectionAborted      = 0xC0000241
	statusConnectionInvalid      = 0xC000023A
	statusNetworkSessionExpired  = 0xC000035C
	statusServerUnavailable      = 0xC0000466
)

// smbReconnectStorage retries idempotent reads after the SMB session expires.
type smbReconnectStorage struct {
	duplicacy.Storage
	mu     sync.Mutex
	reopen func() (duplicacy.Storage, error)
}

func (storage *smbReconnectStorage) retryRead(operation func(duplicacy.Storage) error, canRetry func() bool) error {
	storage.mu.Lock()
	defer storage.mu.Unlock()

	firstErr := operation(storage.Storage)
	if firstErr == nil || !canRetry() || !isRetryableSMBReadError(firstErr) {
		return firstErr
	}

	reconnected, err := storage.reopen()
	if err != nil {
		return fmt.Errorf("SMB read failed (%v); reconnect failed: %w", firstErr, err)
	}
	storage.Storage = reconnected
	duplicacy.LOG_INFO("SMB_RECONNECT", "Reconnected SMB storage after a transient read failure")

	if err := operation(storage.Storage); err != nil {
		return fmt.Errorf("SMB read failed after reconnect (initial error: %v): %w", firstErr, err)
	}
	return nil
}

func (storage *smbReconnectStorage) ListFiles(threadIndex int, dir string) (files []string, sizes []int64, err error) {
	err = storage.retryRead(func(current duplicacy.Storage) error {
		files, sizes, err = current.ListFiles(threadIndex, dir)
		return err
	}, func() bool { return true })
	return files, sizes, err
}

func (storage *smbReconnectStorage) GetFileInfo(threadIndex int, filePath string) (exists bool, isDir bool, size int64, err error) {
	err = storage.retryRead(func(current duplicacy.Storage) error {
		exists, isDir, size, err = current.GetFileInfo(threadIndex, filePath)
		return err
	}, func() bool { return true })
	return exists, isDir, size, err
}

func (storage *smbReconnectStorage) FindChunk(threadIndex int, chunkID string, isFossil bool) (filePath string, exists bool, size int64, err error) {
	err = storage.retryRead(func(current duplicacy.Storage) error {
		filePath, exists, size, err = current.FindChunk(threadIndex, chunkID, isFossil)
		return err
	}, func() bool { return true })
	return filePath, exists, size, err
}

func (storage *smbReconnectStorage) DownloadFile(threadIndex int, filePath string, chunk *duplicacy.Chunk) error {
	initialLength := chunk.GetLength()
	return storage.retryRead(func(current duplicacy.Storage) error {
		return current.DownloadFile(threadIndex, filePath, chunk)
	}, func() bool {
		// Don't append a retry to a partially downloaded chunk.
		return chunk.GetLength() == initialLength
	})
}

func isRetryableSMBReadError(err error) bool {
	var transportError *smb2.TransportError
	if errors.As(err, &transportError) {
		return true
	}
	var responseError *smb2.ResponseError
	if errors.As(err, &responseError) {
		switch responseError.Code {
		case statusInvalidHandle, statusIOTimeout, statusNetworkNameDeleted, statusFileClosed,
			statusPipeBroken, statusUserSessionDeleted, statusConnectionDisconnected,
			statusConnectionReset, statusConnectionAborted, statusConnectionInvalid,
			statusNetworkSessionExpired, statusServerUnavailable:
			return true
		}
		return false
	}
	var timeoutError interface{ Timeout() bool }
	if errors.As(err, &timeoutError) && timeoutError.Timeout() {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed)
}
