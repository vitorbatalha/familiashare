package main

// Lista monitores via DXGI: (adaptador, saída) é exatamente o que o ddagrab do FFmpeg usa.

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procCreateDXGIFactory1 = windows.NewLazySystemDLL("dxgi.dll").NewProc("CreateDXGIFactory1")
	iidIDXGIFactory1       = windows.GUID{Data1: 0x770aae78, Data2: 0xf26f, Data3: 0x4dba, Data4: [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}
)

const (
	vendorNVIDIA = 0x10DE
	vendorAMD    = 0x1002
	vendorIntel  = 0x8086
)

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
	Description                                                   [128]uint16
	VendorID, DeviceID, SubSysID, Revision                        uint32
	DedicatedVideoMemory, DedicatedSystemMemory, SharedSystemMemory uintptr
	Luid                                                          uint64
}

type dxgiOutputDesc struct {
	DeviceName [32]uint16
	Coords     rect
	Attached   int32
	Rotation   uint32
	Monitor    uintptr
}

func listMonitors() []Monitor {
	var factory uintptr
	r, _, _ := procCreateDXGIFactory1.Call(uintptr(unsafe.Pointer(&iidIDXGIFactory1)), uintptr(unsafe.Pointer(&factory)))
	if int32(r) < 0 {
		return nil
	}
	defer comRelease(factory)
	var mons []Monitor
	for a := 0; ; a++ {
		var adapter uintptr
		if r, _, _ := syscall.SyscallN(vt(factory, 12), factory, uintptr(a), uintptr(unsafe.Pointer(&adapter))); int32(r) < 0 {
			break // EnumAdapters1 → DXGI_ERROR_NOT_FOUND
		}
		var ad dxgiAdapterDesc
		syscall.SyscallN(vt(adapter, 8), adapter, uintptr(unsafe.Pointer(&ad)))
		for o := 0; ; o++ {
			var output uintptr
			if r, _, _ := syscall.SyscallN(vt(adapter, 7), adapter, uintptr(o), uintptr(unsafe.Pointer(&output))); int32(r) < 0 {
				break
			}
			var od dxgiOutputDesc
			syscall.SyscallN(vt(output, 7), output, uintptr(unsafe.Pointer(&od)))
			comRelease(output)
			if od.Attached == 0 {
				continue
			}
			w, h := int(od.Coords.R-od.Coords.L), int(od.Coords.B-od.Coords.T)
			mons = append(mons, Monitor{
				Adapter: a, Output: o, GPU: windows.UTF16ToString(ad.Description[:]), Vendor: ad.VendorID,
				Width: w, Height: h, Primary: od.Coords.L == 0 && od.Coords.T == 0,
			})
		}
		comRelease(adapter)
	}
	return mons
}
