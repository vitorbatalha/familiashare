package audio

// Captura de uma árvore de processos com a API de "process loopback" do Windows
// (10 2004+ / 11): ActivateAudioInterfaceAsync em VAD\Process_Loopback.

import (
	"errors"
	"fmt"
	"log"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/vitorbatalha/familiashare/internal/winapi"
	"golang.org/x/sys/windows"
)

var (
	procActivateAudioInterfaceAsync = windows.NewLazySystemDLL("mmdevapi.dll").NewProc("ActivateAudioInterfaceAsync")

	iidIAudioClient        = windows.GUID{Data1: 0x1CB9AD4C, Data2: 0xDBFA, Data3: 0x4c32, Data4: [8]byte{0xB1, 0x78, 0xC2, 0xF5, 0x68, 0xA7, 0x03, 0xB2}}
	iidIAudioCaptureClient = windows.GUID{Data1: 0xC8ADBD64, Data2: 0xE71E, Data3: 0x48a0, Data4: [8]byte{0xA4, 0xDE, 0x18, 0x5C, 0x39, 0x5C, 0xD3, 0x17}}
)

/* ---------- ActivateAudioInterfaceAsync: handler COM de conclusão ---------- */

type activateHandler struct {
	vtbl uintptr
	done chan struct{}
	once sync.Once
}

var activateVtbl = [4]uintptr{
	syscall.NewCallback(func(this, riid, ppv uintptr) uintptr { // QueryInterface: IUnknown, handler, IAgileObject
		*(*uintptr)(unsafe.Pointer(ppv)) = this
		return 0
	}),
	syscall.NewCallback(func(this uintptr) uintptr { return 1 }), // AddRef
	syscall.NewCallback(func(this uintptr) uintptr { return 1 }), // Release
	syscall.NewCallback(func(this, op uintptr) uintptr { // ActivateCompleted
		h := (*activateHandler)(unsafe.Pointer(this))
		h.once.Do(func() { close(h.done) })
		return 0
	}),
}

// keepHandlers mantém vivos os handlers de ativação. O Windows guarda referências
// a eles e chama Release quando "op" é liberado — às vezes bem depois. Se o GC
// coletasse o handler antes, o app fecharia com access violation (bug real,
// visto ao trocar exclusões durante uma transmissão). São poucos bytes por captura.
var keepHandlers sync.Map

type waveFormatEx struct {
	Tag        uint16
	Channels   uint16
	Rate       uint32
	AvgBytes   uint32
	BlockAlign uint16
	Bits       uint16
	Size       uint16
}

// loopbackSource captura uma árvore de processos (incluindo ou excluindo).
type loopbackSource struct {
	key  string
	ring *ring
	stop chan struct{}
	dead atomic.Bool // falhou ou terminou: o planejador tenta de novo
}

func startLoopback(t target) *loopbackSource {
	s := &loopbackSource{key: t.key(), ring: &ring{}, stop: make(chan struct{})}
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		winapi.COMInit()
		if err := s.run(t.pid, t.exclude); err != nil {
			log.Printf("áudio %s: %v", s.key, err)
		}
		s.dead.Store(true)
	}()
	return s
}

func (s *loopbackSource) run(pid uint32, exclude bool) error {
	mode := uint32(0) // PROCESS_LOOPBACK_MODE_INCLUDE_TARGET_PROCESS_TREE
	if exclude {
		mode = 1 // ..._EXCLUDE_TARGET_PROCESS_TREE
	}
	params := &struct{ typ, pid, mode uint32 }{1, pid, mode} // AUDIOCLIENT_ACTIVATION_TYPE_PROCESS_LOOPBACK
	pv := &struct {
		vt         uint16
		r1, r2, r3 uint16
		size       uint32
		_          uint32
		data       uintptr
	}{vt: 65 /* VT_BLOB */, size: 12, data: uintptr(unsafe.Pointer(params))}
	h := &activateHandler{done: make(chan struct{})}
	h.vtbl = uintptr(unsafe.Pointer(&activateVtbl))
	keepHandlers.Store(h, params)
	path, _ := windows.UTF16PtrFromString(`VAD\Process_Loopback`)

	var op uintptr
	r, _, _ := procActivateAudioInterfaceAsync.Call(uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&iidIAudioClient)),
		uintptr(unsafe.Pointer(pv)), uintptr(unsafe.Pointer(h)), uintptr(unsafe.Pointer(&op)))
	if err := winapi.HRErr(r, "ActivateAudioInterfaceAsync"); err != nil {
		return err
	}
	defer winapi.Release(op)
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		return errors.New("timeout ativando captura")
	}
	runtime.KeepAlive(params)
	runtime.KeepAlive(pv)

	var hrAct int32
	var client uintptr
	r, _, _ = syscall.SyscallN(winapi.VT(op, 3), op, uintptr(unsafe.Pointer(&hrAct)), uintptr(unsafe.Pointer(&client)))
	if err := winapi.HRErr(r, "GetActivateResult"); err != nil {
		return err
	}
	if err := winapi.HRErr(uintptr(uint32(hrAct)), "ativação"); err != nil {
		return err
	}
	defer winapi.Release(client)

	wf := &waveFormatEx{Tag: 3 /* IEEE_FLOAT */, Channels: Channels, Rate: SampleRate, AvgBytes: SampleRate * 8, BlockAlign: 8, Bits: 32}
	const flags = 0x00020000 | 0x00040000 | 0x80000000 | 0x08000000 // LOOPBACK | EVENTCALLBACK | AUTOCONVERTPCM | SRC_DEFAULT_QUALITY
	r, _, _ = syscall.SyscallN(winapi.VT(client, 3), client, 0, flags, 200000, 0, uintptr(unsafe.Pointer(wf)), 0)
	if err := winapi.HRErr(r, "IAudioClient.Initialize"); err != nil {
		return err
	}
	ev, _ := windows.CreateEvent(nil, 0, 0, nil)
	defer windows.CloseHandle(ev)
	syscall.SyscallN(winapi.VT(client, 13), client, uintptr(ev)) // SetEventHandle

	var capture uintptr
	r, _, _ = syscall.SyscallN(winapi.VT(client, 14), client, uintptr(unsafe.Pointer(&iidIAudioCaptureClient)), uintptr(unsafe.Pointer(&capture)))
	if err := winapi.HRErr(r, "GetService"); err != nil {
		return err
	}
	defer winapi.Release(capture)
	if r, _, _ = syscall.SyscallN(winapi.VT(client, 10), client); int32(r) < 0 {
		return winapi.HRErr(r, "Start")
	}
	defer syscall.SyscallN(winapi.VT(client, 11), client) // Stop

	for {
		select {
		case <-s.stop:
			return nil
		default:
		}
		windows.WaitForSingleObject(ev, 100)
		for {
			var n uint32
			if r, _, _ := syscall.SyscallN(winapi.VT(capture, 5), capture, uintptr(unsafe.Pointer(&n))); int32(r) < 0 {
				return winapi.HRErr(r, "GetNextPacketSize")
			}
			if n == 0 {
				break
			}
			var data uintptr
			var frames, fl uint32
			if r, _, _ := syscall.SyscallN(winapi.VT(capture, 3), capture, uintptr(unsafe.Pointer(&data)),
				uintptr(unsafe.Pointer(&frames)), uintptr(unsafe.Pointer(&fl)), 0, 0); int32(r) < 0 {
				return winapi.HRErr(r, "GetBuffer")
			}
			if fl&2 != 0 || data == 0 { // AUDCLNT_BUFFERFLAGS_SILENT
				s.ring.pushSilence(int(frames) * Channels)
			} else {
				s.ring.push(unsafe.Slice((*float32)(unsafe.Pointer(data)), int(frames)*Channels))
			}
			syscall.SyscallN(winapi.VT(capture, 4), capture, uintptr(frames)) // ReleaseBuffer
		}
	}
}

func (t target) key() string { return fmt.Sprintf("%d/%v", t.pid, t.exclude) }
