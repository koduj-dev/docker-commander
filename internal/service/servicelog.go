package service

import (
	"io"
	"os"
	"sync"
)

// serviceLogMax is how large the service log grows before it is rotated. One
// rotated copy is kept beside it as <name>.1, so the log takes at most twice this.
const serviceLogMax = 10 << 20

// rotatingLog is the log file of a service that has no console to write to. It
// appends to path and, once the file passes max bytes, moves it to path+".1"
// (replacing an older one) and starts a new file.
type rotatingLog struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

// OpenLogFile opens path as a log that rotates at 10 MiB, keeping one older
// copy as path+".1". For -log-file.
func OpenLogFile(path string) (io.WriteCloser, error) {
	return openRotatingLog(path, serviceLogMax)
}

func openRotatingLog(path string, max int64) (*rotatingLog, error) {
	l := &rotatingLog{path: path, max: max}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *rotatingLog) open() error {
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	l.f, l.size = f, fi.Size()
	return nil
}

// Write appends p, rotating first when the file is already past the limit. A
// failed rotation keeps writing to the current file: a log too large beats a log
// lost.
func (l *rotatingLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.size >= l.max {
		// Closed before the rename, which Windows refuses on an open file.
		if err := l.f.Close(); err == nil {
			_ = os.Rename(l.path, l.path+".1")
			if err := l.open(); err != nil {
				return 0, err
			}
		}
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	return n, err
}

func (l *rotatingLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}
