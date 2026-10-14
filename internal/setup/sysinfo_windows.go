//go:build windows

package setup

import (
	"unsafe"

	"github.com/trysoft/whatomate/internal/diskspace"
	"golang.org/x/sys/windows"
)

type hostSystem struct{}

type memoryStatusEx struct {
	Length, MemoryLoad                                 uint32
	TotalPhys, AvailPhys, TotalPageFile, AvailPageFile uint64
	TotalVirtual, AvailVirtual, AvailExtendedVirtual   uint64
}

var globalMemoryStatusEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

func (hostSystem) TotalMemory() (uint64, error) {
	m := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	if r, _, err := globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m))); r == 0 {
		return 0, err
	}
	return m.TotalPhys, nil
}

func (hostSystem) FreeDisk(path string) (uint64, error) { return diskspace.Free(path) }
