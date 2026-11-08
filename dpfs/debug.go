package dpfs

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

var debugLogMu sync.Mutex

func debugLog(writer io.Writer, enabled bool, logID string, format string, args ...interface{}) {
	if !enabled {
		return
	}
	if writer == nil {
		writer = os.Stderr
	}
	debugLogMu.Lock()
	defer debugLogMu.Unlock()
	fmt.Fprintf(writer, "%s DPFS_DEBUG %s %s\n", time.Now().Format("2006-01-02 15:04:05.000"), logID, fmt.Sprintf(format, args...))
}
