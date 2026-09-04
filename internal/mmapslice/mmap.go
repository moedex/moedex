package mmapslice

import (
	"fmt"
	"os"
	"syscall"
)

// Mapping is a read-only mmap of a whole file. Close is idempotent.
//
// The file descriptor is closed immediately after mapping: on both darwin and
// linux the mapping keeps the underlying file alive on its own, so holding the
// fd would only leak a descriptor per open sidecar.
type Mapping struct {
	data []byte
}

// Open maps path read-only in its entirety. An empty file is an error: there is
// nothing to address, and every moedex format has a mandatory header.
func Open(path string) (*Mapping, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := fi.Size()
	if size <= 0 {
		return nil, fmt.Errorf("mmapslice: %s is empty", path)
	}
	if size > int64(^uint(0)>>1) {
		return nil, fmt.Errorf("mmapslice: %s is too large to map (%d bytes)", path, size)
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, int(size), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("mmapslice: mmap %s: %w", path, err)
	}
	return &Mapping{data: data}, nil
}

// Bytes returns the mapped region. The slice is valid only until Close.
func (m *Mapping) Bytes() []byte {
	if m == nil {
		return nil
	}
	return m.data
}

// Close unmaps the region. It is safe to call more than once.
func (m *Mapping) Close() error {
	if m == nil || m.data == nil {
		return nil
	}
	err := syscall.Munmap(m.data)
	m.data = nil
	return err
}
