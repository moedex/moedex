//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package snapshot

import "os"

func acquireBuildLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if os.IsExist(err) {
		return nil, ErrBuildLocked
	}
	return f, err
}

func releaseBuildLock(f *os.File) {
	if f == nil {
		return
	}
	path := f.Name()
	_ = f.Close()
	_ = os.Remove(path)
}
