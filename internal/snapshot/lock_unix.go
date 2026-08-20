//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package snapshot

import (
	"errors"
	"os"
	"syscall"
)

func acquireBuildLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrBuildLocked
		}
		return nil, err
	}
	return f, nil
}

func releaseBuildLock(f *os.File) {
	if f == nil {
		return
	}
	_ = f.Truncate(0)
	_ = f.Sync()
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}
