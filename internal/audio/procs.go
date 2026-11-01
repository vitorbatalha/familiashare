package audio

// Processos (para saber quem é "filho" de quem) e sessões de áudio do Windows
// (para saber quais apps estão tocando som).

import (
	"os"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"unsafe"

	"github.com/vitorbatalha/familiashare/internal/winapi"
	"golang.org/x/sys/windows"
)

var (
	clsidMMDeviceEnumerator  = windows.GUID{Data1: 0xBCDE0395, Data2: 0xE52F, Data3: 0x467C, Data4: [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}}
	iidIMMDeviceEnumerator   = windows.GUID{Data1: 0xA95664D2, Data2: 0x9614, Data3: 0x4F35, Data4: [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}}
	iidIAudioSessionManager2 = windows.GUID{Data1: 0x77AA99A0, Data2: 0x1BD6, Data3: 0x484F, Data4: [8]byte{0x8B, 0xC7, 0x2C, 0x65, 0x4C, 0x9A, 0x9B, 0x6F}}
	iidIAudioSessionControl2 = windows.GUID{Data1: 0xbfb7ff88, Data2: 0x7239, Data3: 0x4fc9, Data4: [8]byte{0x8f, 0xa2, 0x07, 0xc9, 0x50, 0xbe, 0x9c, 0x6d}}
)

type procInfo struct {
	name   string // executável em minúsculas, ex.: "discord.exe"
	parent uint32
}

// processes tira uma foto dos processos em execução.
func processes() map[uint32]procInfo {
	m := map[uint32]procInfo{}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return m
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		m[e.ProcessID] = procInfo{strings.ToLower(windows.UTF16ToString(e.ExeFile[:])), e.ParentProcessID}
	}
	return m
}

// ancestors devolve a cadeia de pais de pid (sem incluir pid).
func ancestors(procs map[uint32]procInfo, pid uint32) []uint32 {
	var out []uint32
	for i := 0; i < 32; i++ {
		p, ok := procs[pid]
		if !ok || p.parent == 0 || p.parent == pid {
			break
		}
		if _, ok := procs[p.parent]; !ok {
			break
		}
		pid = p.parent
		out = append(out, pid)
	}
	return out
}

// sessionPIDs lista os processos com sessão de áudio no dispositivo de saída padrão.
// Precisa rodar numa thread com COM inicializado.
func sessionPIDs() ([]uint32, error) {
	var dev, mgr, senum uintptr
	enum, err := winapi.CoCreateInstance(&clsidMMDeviceEnumerator, &iidIMMDeviceEnumerator)
	if err != nil {
		return nil, err
	}
	defer winapi.Release(enum)
	if r, _, _ := syscall.SyscallN(winapi.VT(enum, 4), enum, 0, 0, uintptr(unsafe.Pointer(&dev))); int32(r) < 0 {
		return nil, winapi.HRErr(r, "GetDefaultAudioEndpoint")
	}
	defer winapi.Release(dev)
	if r, _, _ := syscall.SyscallN(winapi.VT(dev, 3), dev, uintptr(unsafe.Pointer(&iidIAudioSessionManager2)), 0x17, 0, uintptr(unsafe.Pointer(&mgr))); int32(r) < 0 {
		return nil, winapi.HRErr(r, "Activate SessionManager2")
	}
	defer winapi.Release(mgr)
	if r, _, _ := syscall.SyscallN(winapi.VT(mgr, 5), mgr, uintptr(unsafe.Pointer(&senum))); int32(r) < 0 {
		return nil, winapi.HRErr(r, "GetSessionEnumerator")
	}
	defer winapi.Release(senum)
	var count int32
	syscall.SyscallN(winapi.VT(senum, 3), senum, uintptr(unsafe.Pointer(&count)))
	var pids []uint32
	for i := int32(0); i < count; i++ {
		var ctl, ctl2 uintptr
		if r, _, _ := syscall.SyscallN(winapi.VT(senum, 4), senum, uintptr(i), uintptr(unsafe.Pointer(&ctl))); int32(r) < 0 {
			continue
		}
		if r, _, _ := syscall.SyscallN(winapi.VT(ctl, 0), ctl, uintptr(unsafe.Pointer(&iidIAudioSessionControl2)), uintptr(unsafe.Pointer(&ctl2))); int32(r) >= 0 {
			var state int32
			var pid uint32
			syscall.SyscallN(winapi.VT(ctl2, 3), ctl2, uintptr(unsafe.Pointer(&state)))
			syscall.SyscallN(winapi.VT(ctl2, 14), ctl2, uintptr(unsafe.Pointer(&pid)))
			if state != 2 && pid != 0 { // ignora sessões expiradas e "sons do sistema"
				pids = append(pids, pid)
			}
			winapi.Release(ctl2)
		}
		winapi.Release(ctl)
	}
	return pids, nil
}

// AppNames lista os executáveis que têm sessão de áudio agora (para a interface
// sugerir o que excluir).
func AppNames() []string {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	winapi.COMInit()
	pids, _ := sessionPIDs()
	procs := processes()
	self := uint32(os.Getpid())
	seen := map[string]bool{}
	var names []string
	for _, pid := range pids {
		p, ok := procs[pid]
		if !ok || pid == self || seen[p.name] {
			continue
		}
		seen[p.name] = true
		names = append(names, p.name)
	}
	sort.Strings(names)
	return names
}
