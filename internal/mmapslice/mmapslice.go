// Package mmapslice reinterprets a byte range — typically an mmap'd file — as a
// typed slice without copying, and opens the mappings themselves.
//
// This is the ONLY package in moedex that uses unsafe. It exists so that the
// mmap-backed sidecars (internal/tokenindex, internal/embed) share one audited
// implementation of the same three guards: length, alignment, and byte order.
//
// # Why unsafe rather than encoding/binary
//
// The dense arm scans every stored vector on a query — 953,451 chunks x 768
// dimensions is ~732M float32 reads per search. Decoding each through
// binary.LittleEndian.Uint32 costs a function call and a bounds check per
// element on the hottest loop in the system. Aliasing the mapping is what makes
// mmap-backed vectors viable at all.
//
// # Safety contract
//
// A returned slice ALIASES the caller's bytes. It is valid only while those
// bytes are: (a) still mapped, and (b) not written. Every caller in moedex maps
// PROT_READ and holds the Mapping for the returned slice's whole lifetime.
// Reading a slice after its Mapping is closed dereferences unmapped memory and
// will crash the process, exactly as diskstore's posting sub-slices already do.
package mmapslice

import (
	"errors"
	"fmt"
	"math"
	"unsafe"
)

var (
	// ErrShort means the byte range is too small to hold n elements.
	ErrShort = errors.New("mmapslice: buffer too short")
	// ErrAlignment means the byte range does not start on the element's
	// natural alignment boundary, so aliasing it would be undefined.
	ErrAlignment = errors.New("mmapslice: misaligned buffer")
	// ErrOverflow means n * elementSize does not fit in an int.
	ErrOverflow = errors.New("mmapslice: element count overflows")
)

func init() {
	// Aliasing bytes as multi-byte integers is only correct when the host byte
	// order matches the format's declared little-endian order. Every moedex
	// target (darwin/linux on amd64/arm64) is little-endian; fail loudly rather
	// than silently returning byte-swapped data if that ever stops being true.
	var probe uint16 = 1
	if *(*byte)(unsafe.Pointer(&probe)) != 1 {
		panic("mmapslice: big-endian architecture is not supported")
	}
}

// check validates n elements of size bytes against b and returns the base
// pointer. It is the single place all four reinterpreters get their guards.
func check(b []byte, n, size int) (unsafe.Pointer, error) {
	if n < 0 {
		return nil, fmt.Errorf("mmapslice: negative element count %d", n)
	}
	if n == 0 {
		return nil, nil
	}
	if n > math.MaxInt/size {
		return nil, fmt.Errorf("%w: %d elements of %d bytes", ErrOverflow, n, size)
	}
	need := n * size
	if len(b) < need {
		return nil, fmt.Errorf("%w: need %d bytes, have %d", ErrShort, need, len(b))
	}
	p := unsafe.Pointer(&b[0])
	if size > 1 && uintptr(p)%uintptr(size) != 0 {
		return nil, fmt.Errorf("%w: %d-byte alignment required", ErrAlignment, size)
	}
	return p, nil
}

// typed reinterprets the first n elements of b as a []T without copying. Size
// and cast both derive from T, so a size/cast mismatch is unrepresentable —
// which is the point: this is the only unsafe in the codebase.
func typed[T any](b []byte, n int) ([]T, error) {
	var zero T
	size := int(unsafe.Sizeof(zero))
	p, err := check(b, n, size)
	if p == nil {
		return nil, err
	}
	return unsafe.Slice((*T)(p), n), nil
}

// Uint32s reinterprets the first n*4 bytes of b as a []uint32 without copying.
func Uint32s(b []byte, n int) ([]uint32, error) {
	return typed[uint32](b, n)
}

// Uint64s reinterprets the first n*8 bytes of b as a []uint64 without copying.
func Uint64s(b []byte, n int) ([]uint64, error) {
	return typed[uint64](b, n)
}

// Float32s reinterprets the first n*4 bytes of b as a []float32 without copying.
func Float32s(b []byte, n int) ([]float32, error) {
	return typed[float32](b, n)
}

// Int8s reinterprets the first n bytes of b as a []int8 without copying. Single
// bytes have no alignment constraint.
func Int8s(b []byte, n int) ([]int8, error) {
	return typed[int8](b, n)
}
