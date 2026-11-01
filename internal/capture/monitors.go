// Package capture descobre o que pode ser capturado (monitores e GPUs).
// A captura em si é feita pelo FFmpeg (filtro ddagrab); ver internal/stream.
package capture

import (
	"syscall"
	"unsafe"

	"github.com/vitorbatalha/familiashare/internal/winapi"
	"golang.org/x/sys/windows"
)

var (
	procCreateDXGIFactory1 = windows.NewLazySystemDLL("dxgi.dll").NewProc("CreateDXGIFactory1")
	iidIDXGIFactory1       = windows.GUID{Data1: 0x770aae78, Data2: 0xf26f, Data3: 0x4dba, Data4: [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}
)

// IDs PCI dos fabricantes de GPU.
const (
	VendorNVIDIA = 0x10DE
	VendorAMD    = 0x1002
	VendorIntel  = 0x8086
)

// Monitor é uma saída de vídeo. (Adapter, Output) é exatamente o par que o
// ddagrab do FFmpeg usa para escolher o que capturar.
type Monitor struct {
	Adapter int    `json:"adapter"`
	Output  int    `json:"output"`
	GPU     string `json:"gpu"`
	Vendor  uint32 `json:"vendor"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Primary bool   `json:"primary"`
}

type dxgiAdapterDesc struct {
	Description                                                     [128]uint16
	VendorID, DeviceID, SubSysID, Revision                          uint32
	DedicatedVideoMemory, DedicatedSystemMemory, SharedSystemMemory uintptr
	Luid                                                            uint64
}

type dxgiOutputDesc struct {
	DeviceName [32]uint16
	Coords     winapi.Rect
	Attached   int32
	Rotation   uint32
	Monitor    uintptr
}

// ListMonitors lista os monitores conectados, na ordem do DXGI.
func ListMonitors() []Monitor {
	var factory uintptr
	r, _, _ := procCreateDXGIFactory1.Call(uintptr(unsafe.Pointer(&iidIDXGIFactory1)), uintptr(unsafe.Pointer(&factory)))
	if int32(r) < 0 {
		return nil
	}
	defer winapi.Release(factory)
	var mons []Monitor
	for a := 0; ; a++ {
		var adapter uintptr
		if r, _, _ := syscall.SyscallN(winapi.VT(factory, 12), factory, uintptr(a), uintptr(unsafe.Pointer(&adapter))); int32(r) < 0 {
			break // EnumAdapters1 → DXGI_ERROR_NOT_FOUND
		}
		var ad dxgiAdapterDesc
		syscall.SyscallN(winapi.VT(adapter, 8), adapter, uintptr(unsafe.Pointer(&ad)))
		for o := 0; ; o++ {
			var output uintptr
			if r, _, _ := syscall.SyscallN(winapi.VT(adapter, 7), adapter, uintptr(o), uintptr(unsafe.Pointer(&output))); int32(r) < 0 {
				break
			}
			var od dxgiOutputDesc
			syscall.SyscallN(winapi.VT(output, 7), output, uintptr(unsafe.Pointer(&od)))
			winapi.Release(output)
			if od.Attached == 0 {
				continue
			}
			w, h := int(od.Coords.R-od.Coords.L), int(od.Coords.B-od.Coords.T)
			mons = append(mons, Monitor{
				Adapter: a, Output: o, GPU: windows.UTF16ToString(ad.Description[:]), Vendor: ad.VendorID,
				Width: w, Height: h, Primary: od.Coords.L == 0 && od.Coords.T == 0,
			})
		}
		winapi.Release(adapter)
	}
	return mons
}
