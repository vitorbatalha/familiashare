// Package winapi reúne as chamadas diretas à API do Windows (COM, janelas,
// processos) usadas pelo resto do app, para que nenhum outro pacote precise
// lidar com ponteiros e vtables.
package winapi

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
)

// VT devolve o ponteiro da função de índice i na vtable do objeto COM obj.
// Use com syscall.SyscallN(winapi.VT(obj, i), obj, args...).
func VT(obj uintptr, i int) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(obj))
	return *(*uintptr)(unsafe.Pointer(vtbl + uintptr(i)*unsafe.Sizeof(uintptr(0))))
}

// HRErr converte um HRESULT de falha em erro (nil se for sucesso).
func HRErr(r uintptr, what string) error {
	if int32(r) < 0 {
		return fmt.Errorf("%s: HRESULT 0x%08X", what, uint32(r))
	}
	return nil
}

// Release chama IUnknown::Release (ignora objetos nulos).
func Release(obj uintptr) {
	if obj != 0 {
		syscall.SyscallN(VT(obj, 2), obj)
	}
}

// COMInit inicializa o COM na thread atual (multithreaded). A goroutine deve
// estar presa à thread (runtime.LockOSThread).
func COMInit() { procCoInitializeEx.Call(0, 0) }

// CoCreateInstance cria um objeto COM (CLSCTX_ALL) e devolve a interface pedida.
func CoCreateInstance(clsid, iid *windows.GUID) (uintptr, error) {
	var obj uintptr
	r, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(clsid)), 0, 0x17,
		uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&obj)))
	if err := HRErr(r, "CoCreateInstance"); err != nil {
		return 0, err
	}
	return obj, nil
}
