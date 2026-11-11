package dpfs

import (
	"fmt"

	duplicacy "github.com/gilbertchen/duplicacy/src"
)

func fetchChunk(config *duplicacy.Config, storage duplicacy.Storage, chunkHash string) ([]byte, error) {
	chunkID := config.GetChunkIDFromHash(chunkHash)
	chunkPath, exists, _, err := storage.FindChunk(0, chunkID, false, false)
	if err != nil {
		return nil, fmt.Errorf("find chunk %s: %w", chunkID, err)
	}
	if !exists {
		chunkPath, exists, _, err = storage.FindChunk(0, chunkID, true, false)
		if err != nil {
			return nil, fmt.Errorf("find fossil chunk %s: %w", chunkID, err)
		}
	}
	if !exists {
		return nil, fmt.Errorf("chunk %s is missing from storage", chunkID)
	}

	chunk := config.GetChunk()
	defer config.PutChunk(chunk)
	chunk.Reset(false)
	if err := storage.DownloadFile(0, chunkPath, chunk); err != nil {
		return nil, fmt.Errorf("download chunk %s: %w", chunkID, err)
	}
	if err, _ := chunk.Decrypt(config.ChunkKey, chunkHash); err != nil {
		return nil, fmt.Errorf("decrypt chunk %s: %w", chunkID, err)
	}
	if actualID := chunk.GetID(); actualID != chunkID {
		return nil, fmt.Errorf("chunk %s failed integrity check (got %s)", chunkID, actualID)
	}
	return append([]byte(nil), chunk.GetBytes()...), nil
}
