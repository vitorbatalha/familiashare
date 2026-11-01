package main

// Captura de áudio do sistema com exclusão de aplicativos, usando a API de
// "process loopback" do Windows (10 2004+ / 11).
//
// O Windows permite, por captura, incluir OU excluir UMA árvore de processos:
//   - nenhum app excluído → exclui a própria árvore do tela (= áudio do sistema inteiro)
//   - um app excluído     → exclui a árvore dele (ex.: Discord e todos os processos filhos)
//   - vários excluídos    → captura cada app permitido separadamente e mistura

import (
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	sampleRate = 48000
	channels   = 2
	chunk      = sampleRate / 100 // 10 ms
)

var (
	procActivateAudioInterfaceAsync = windows.NewLazySystemDLL("mmdevapi.dll").NewProc("ActivateAudioInterfaceAsync")

	iidIAudioClient          = windows.GUID{Data1: 0x1CB9AD4C, Data2: 0xDBFA, Data3: 0x4c32, Data4: [8]byte{0xB1, 0x78, 0xC2, 0xF5, 0x68, 0xA7, 0x03, 0xB2}}
	iidIAudioCaptureClient   = windows.GUID{Data1: 0xC8ADBD64, Data2: 0xE71E, Data3: 0x48a0, Data4: [8]byte{0xA4, 0xDE, 0x18, 0x5C, 0x39, 0x5C, 0xD3, 0x17}}
	clsidMMDeviceEnumerator  = windows.GUID{Data1: 0xBCDE0395, Data2: 0xE52F, Data3: 0x467C, Data4: [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}}
	iidIMMDeviceEnumerator   = windows.GUID{Data1: 0xA95664D2, Data2: 0x9614, Data3: 0x4F35, Data4: [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}}
	iidIAudioSessionManager2 = windows.GUID{Data1: 0x77AA99A0, Data2: 0x1BD6, Data3: 0x484F, Data4: [8]byte{0x8B, 0xC7, 0x2C, 0x65, 0x4C, 0x9A, 0x9B, 0x6F}}
	iidIAudioSessionControl2 = windows.GUID{Data1: 0xbfb7ff88, Data2: 0x7239, Data3: 0x4fc9, Data4: [8]byte{0x8f, 0xa2, 0x07, 0xc9, 0x50, 0xbe, 0x9c, 0x6d}}
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

var keepHandlers sync.Map // handlers de ativação (ver loopbackSource.run)

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
	dead atomic.Bool // falhou ou terminou: o planner tenta de novo
}

func startLoopback(pid uint32, exclude bool) *loopbackSource {
	s := &loopbackSource{key: fmt.Sprintf("%d/%v", pid, exclude), ring: &ring{}, stop: make(chan struct{})}
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		comInit()
		if err := s.run(pid, exclude); err != nil {
			log.Printf("áudio %s: %v", s.key, err)
		}
		s.dead.Store(true)
	}()
	return s
}

func (s *loopbackSource) run(pid uint32, exclude bool) error {
	mode := uint32(0) // INCLUDE_TARGET_PROCESS_TREE
	if exclude {
		mode = 1
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
	// O Windows guarda referências ao handler e chama Release nele quando "op" é
	// liberado — às vezes bem depois. Se o GC coletar h antes, o app fecha com
	// access violation. São poucos bytes por captura: ficam vivos para sempre.
	keepHandlers.Store(h, params)
	path, _ := windows.UTF16PtrFromString(`VAD\Process_Loopback`)

	var op uintptr
	r, _, _ := procActivateAudioInterfaceAsync.Call(uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&iidIAudioClient)),
		uintptr(unsafe.Pointer(pv)), uintptr(unsafe.Pointer(h)), uintptr(unsafe.Pointer(&op)))
	if err := hrErr(r, "ActivateAudioInterfaceAsync"); err != nil {
		return err
	}
	defer comRelease(op)
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		return errors.New("timeout ativando captura")
	}
	runtime.KeepAlive(params)
	runtime.KeepAlive(pv)

	var hrAct int32
	var client uintptr
	r, _, _ = syscall.SyscallN(vt(op, 3), op, uintptr(unsafe.Pointer(&hrAct)), uintptr(unsafe.Pointer(&client)))
	if err := hrErr(r, "GetActivateResult"); err != nil {
		return err
	}
	if err := hrErr(uintptr(uint32(hrAct)), "ativação"); err != nil {
		return err
	}
	defer comRelease(client)

	wf := &waveFormatEx{Tag: 3 /* IEEE_FLOAT */, Channels: channels, Rate: sampleRate, AvgBytes: sampleRate * 8, BlockAlign: 8, Bits: 32}
	const flags = 0x00020000 | 0x00040000 | 0x80000000 | 0x08000000 // LOOPBACK | EVENTCALLBACK | AUTOCONVERTPCM | SRC_DEFAULT_QUALITY
	r, _, _ = syscall.SyscallN(vt(client, 3), client, 0, flags, 200000, 0, uintptr(unsafe.Pointer(wf)), 0)
	if err := hrErr(r, "IAudioClient.Initialize"); err != nil {
		return err
	}
	ev, _ := windows.CreateEvent(nil, 0, 0, nil)
	defer windows.CloseHandle(ev)
	syscall.SyscallN(vt(client, 13), client, uintptr(ev))

	var capture uintptr
	r, _, _ = syscall.SyscallN(vt(client, 14), client, uintptr(unsafe.Pointer(&iidIAudioCaptureClient)), uintptr(unsafe.Pointer(&capture)))
	if err := hrErr(r, "GetService"); err != nil {
		return err
	}
	defer comRelease(capture)
	if r, _, _ = syscall.SyscallN(vt(client, 10), client); int32(r) < 0 {
		return hrErr(r, "Start")
	}
	defer syscall.SyscallN(vt(client, 11), client)

	for {
		select {
		case <-s.stop:
			return nil
		default:
		}
		windows.WaitForSingleObject(ev, 100)
		for {
			var n uint32
			if r, _, _ := syscall.SyscallN(vt(capture, 5), capture, uintptr(unsafe.Pointer(&n))); int32(r) < 0 {
				return hrErr(r, "GetNextPacketSize")
			}
			if n == 0 {
				break
			}
			var data uintptr
			var frames, fl uint32
			if r, _, _ := syscall.SyscallN(vt(capture, 3), capture, uintptr(unsafe.Pointer(&data)),
				uintptr(unsafe.Pointer(&frames)), uintptr(unsafe.Pointer(&fl)), 0, 0); int32(r) < 0 {
				return hrErr(r, "GetBuffer")
			}
			if fl&2 != 0 || data == 0 { // AUDCLNT_BUFFERFLAGS_SILENT
				s.ring.pushSilence(int(frames) * channels)
			} else {
				s.ring.push(unsafe.Slice((*float32)(unsafe.Pointer(data)), int(frames)*channels))
			}
			syscall.SyscallN(vt(capture, 4), capture, uintptr(frames))
		}
	}
}

/* ---------- Buffer por fonte (absorve jitter e diferença de clock) ---------- */

type ring struct {
	mu     sync.Mutex
	buf    []float32
	primed bool
}

const (
	primeSamples = chunk * channels * 2  // 20 ms antes de começar a tocar
	maxSamples   = chunk * channels * 10 // >100 ms acumulado → descarta o excesso
	keepSamples  = chunk * channels * 4
)

func (r *ring) push(s []float32) {
	r.mu.Lock()
	r.buf = append(r.buf, s...)
	if len(r.buf) > maxSamples {
		r.buf = append(r.buf[:0], r.buf[len(r.buf)-keepSamples:]...)
	}
	r.mu.Unlock()
}

func (r *ring) pushSilence(n int) {
	r.mu.Lock()
	for i := 0; i < n; i++ {
		r.buf = append(r.buf, 0)
	}
	if len(r.buf) > maxSamples {
		r.buf = append(r.buf[:0], r.buf[len(r.buf)-keepSamples:]...)
	}
	r.mu.Unlock()
}

func (r *ring) mixInto(out []float32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.primed {
		if len(r.buf) < primeSamples {
			return
		}
		r.primed = true
	}
	n := min(len(out), len(r.buf))
	for i := 0; i < n; i++ {
		out[i] += r.buf[i]
	}
	r.buf = append(r.buf[:0], r.buf[n:]...)
	if n < len(out) {
		r.primed = false // underflow: espera encher de novo
	}
}

/* ---------- Processos e sessões de áudio ---------- */

type procInfo struct {
	name   string
	parent uint32
}

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
	var enum, dev, mgr, senum uintptr
	r, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, 0x17,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)), uintptr(unsafe.Pointer(&enum)))
	if err := hrErr(r, "MMDeviceEnumerator"); err != nil {
		return nil, err
	}
	defer comRelease(enum)
	if r, _, _ = syscall.SyscallN(vt(enum, 4), enum, 0, 0, uintptr(unsafe.Pointer(&dev))); int32(r) < 0 {
		return nil, hrErr(r, "GetDefaultAudioEndpoint")
	}
	defer comRelease(dev)
	if r, _, _ = syscall.SyscallN(vt(dev, 3), dev, uintptr(unsafe.Pointer(&iidIAudioSessionManager2)), 0x17, 0, uintptr(unsafe.Pointer(&mgr))); int32(r) < 0 {
		return nil, hrErr(r, "Activate SessionManager2")
	}
	defer comRelease(mgr)
	if r, _, _ = syscall.SyscallN(vt(mgr, 5), mgr, uintptr(unsafe.Pointer(&senum))); int32(r) < 0 {
		return nil, hrErr(r, "GetSessionEnumerator")
	}
	defer comRelease(senum)
	var count int32
	syscall.SyscallN(vt(senum, 3), senum, uintptr(unsafe.Pointer(&count)))
	var pids []uint32
	for i := int32(0); i < count; i++ {
		var ctl, ctl2 uintptr
		if r, _, _ := syscall.SyscallN(vt(senum, 4), senum, uintptr(i), uintptr(unsafe.Pointer(&ctl))); int32(r) < 0 {
			continue
		}
		if r, _, _ := syscall.SyscallN(vt(ctl, 0), ctl, uintptr(unsafe.Pointer(&iidIAudioSessionControl2)), uintptr(unsafe.Pointer(&ctl2))); int32(r) >= 0 {
			var state int32
			var pid uint32
			syscall.SyscallN(vt(ctl2, 3), ctl2, uintptr(unsafe.Pointer(&state)))
			syscall.SyscallN(vt(ctl2, 14), ctl2, uintptr(unsafe.Pointer(&pid)))
			if state != 2 && pid != 0 { // ignora sessões expiradas e "sons do sistema"
				pids = append(pids, pid)
			}
			comRelease(ctl2)
		}
		comRelease(ctl)
	}
	return pids, nil
}

// audioAppNames: nomes dos executáveis que têm sessão de áudio agora (para a interface).
func audioAppNames() []string {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	comInit()
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

/* ---------- Motor: decide o que capturar, mistura e entrega PCM a cada 10 ms ---------- */

type target struct {
	pid     uint32
	exclude bool
}

func (t target) key() string { return fmt.Sprintf("%d/%v", t.pid, t.exclude) }

type audioEngine struct {
	mu      sync.Mutex
	excl    map[string]bool
	sources map[string]*loopbackSource
	mode    string
	stop    chan struct{}
	wake    chan struct{}
}

func newAudioEngine(exclude []string) *audioEngine {
	a := &audioEngine{sources: map[string]*loopbackSource{}, stop: make(chan struct{}), wake: make(chan struct{}, 1)}
	a.SetExclusions(exclude)
	go a.planner()
	return a
}

func (a *audioEngine) SetExclusions(names []string) {
	m := map[string]bool{}
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" {
			continue
		}
		if !strings.HasSuffix(n, ".exe") {
			n += ".exe"
		}
		m[n] = true
	}
	a.mu.Lock()
	a.excl = m
	a.mu.Unlock()
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *audioEngine) Mode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

func (a *audioEngine) plan(sessions []uint32) ([]target, string) {
	a.mu.Lock()
	excl := a.excl
	a.mu.Unlock()
	self := uint32(os.Getpid())
	all := []target{{self, true}}
	if len(excl) == 0 {
		return all, "sistema inteiro"
	}
	procs := processes()
	isExcluded := func(pid uint32) bool {
		if pid == self || excl[procs[pid].name] {
			return true
		}
		for _, anc := range ancestors(procs, pid) {
			if anc == self || excl[procs[anc].name] {
				return true
			}
		}
		return false
	}
	// raízes das árvores excluídas que estão rodando
	var roots []uint32
	for pid, p := range procs {
		if !excl[p.name] {
			continue
		}
		parentExcluded := false
		if pp, ok := procs[p.parent]; ok && excl[pp.name] && p.parent != pid {
			parentExcluded = true
		}
		if !parentExcluded {
			roots = append(roots, pid)
		}
	}
	if len(roots) == 0 {
		return all, "sistema inteiro (apps excluídos não estão abertos)"
	}
	// O próprio tela também precisa ficar de fora (é ele que toca o som de quem você
	// assiste — senão vira eco). Com ele, são ≥ 2 árvores excluídas, e o Windows só
	// exclui uma por captura: então inclui cada árvore permitida que tem sessão de áudio.
	excludedDesc := map[uint32]bool{} // processos que têm algum descendente excluído
	for pid := range procs {
		if pid == self || excl[procs[pid].name] {
			for _, anc := range ancestors(procs, pid) {
				excludedDesc[anc] = true
			}
		}
	}
	cand := map[uint32]bool{}
	for _, pid := range sessions {
		if !isExcluded(pid) && !excludedDesc[pid] {
			cand[pid] = true
		}
	}
	var ts []target
	for pid := range cand {
		covered := false
		for _, anc := range ancestors(procs, pid) {
			if cand[anc] {
				covered = true
				break
			}
		}
		if !covered {
			ts = append(ts, target{pid, false})
		}
	}
	return ts, fmt.Sprintf("%d apps (exceto %d excluídos)", len(ts), len(roots))
}

// Run roda até Stop; out recebe blocos de 10 ms (float32 estéreo intercalado).
func (a *audioEngine) Run(out func([]float32) error) {
	buf := make([]float32, chunk*channels)
	next := time.Now()
	for {
		select {
		case <-a.stop:
			return
		default:
		}
		next = next.Add(10 * time.Millisecond)
		if d := time.Until(next); d > 0 {
			time.Sleep(d)
		} else if d < -200*time.Millisecond {
			next = time.Now()
		}
		clear(buf)
		a.mu.Lock()
		for _, s := range a.sources {
			s.ring.mixInto(buf)
		}
		a.mu.Unlock()
		for i, v := range buf {
			buf[i] = float32(math.Max(-1, math.Min(1, float64(v))))
		}
		if err := out(buf); err != nil {
			return
		}
	}
}

func (a *audioEngine) planner() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	comInit()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		sessions, _ := sessionPIDs()
		targets, mode := a.plan(sessions)
		want := map[string]target{}
		for _, t := range targets {
			want[t.key()] = t
		}
		a.mu.Lock()
		a.mode = mode
		for k, s := range a.sources {
			if _, ok := want[k]; !ok || s.dead.Load() {
				close(s.stop)
				delete(a.sources, k)
			}
		}
		for k, t := range want {
			if _, ok := a.sources[k]; !ok {
				a.sources[k] = startLoopback(t.pid, t.exclude)
			}
		}
		a.mu.Unlock()
		select {
		case <-a.stop:
			a.mu.Lock()
			for k, s := range a.sources {
				close(s.stop)
				delete(a.sources, k)
			}
			a.mu.Unlock()
			return
		case <-a.wake:
		case <-t.C:
		}
	}
}

func (a *audioEngine) Stop() {
	select {
	case <-a.stop:
	default:
		close(a.stop)
	}
}
