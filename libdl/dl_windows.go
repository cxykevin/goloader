//go:build windows
// +build windows

package libdl

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"
)

func Open(dllName string) (uintptr, error) {
	dll, err := syscall.LoadDLL(dllName)
	if err != nil {
		return 0, fmt.Errorf("failed to open %s", dllName)
	}
	return uintptr(unsafe.Pointer(dll)), nil
}

func LookupSymbol(h uintptr, symName string) (uintptr, error) {
	// A dynamic import name may carry a version suffix introduced by the C
	// toolchain ("name%version"); Windows exports are looked up by plain name.
	if index := strings.Index(symName, "%"); index >= 0 {
		symName = symName[:index]
	}
	dll := (*syscall.DLL)(unsafe.Pointer(h))
	proc, err := dll.FindProc(symName)
	if err != nil {
		return 0, fmt.Errorf("failed to lookup symbol %s: %w", symName, err)
	}
	return proc.Addr(), nil
}
