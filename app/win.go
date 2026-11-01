package main

// Helpers nativos do Windows: COM, janela, job object para processos filhos.

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                     = windows.NewLazySystemDLL("user32.dll")
	procMessageBoxW            = user32.NewProc("MessageBoxW")
	procSetProcessDpiAwareness = user32.NewProc("SetProcessDpiAwarenessContext")
	procGetWindowLongPtrW      = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtrW      = user32.NewProc("SetWindowLongPtrW")
	procGetWindowRect          = user32.NewProc("GetWindowRect")
	procSetWindowPos           = user32.NewProc("SetWindowPos")
	procMonitorFromWindow      = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW        = user32.NewProc("GetMonitorInfoW")

	ole32              = windows.NewLazySystemDLL("ole32.dll")
	procCoInitializeEx = ole32.NewProc("CoInitializeEx")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
)

// vt retorna o ponteiro da função de índice i na vtable de um objeto COM.
func vt(obj uintptr, i int) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(obj))
	return *(*uintptr)(unsafe.Pointer(vtbl + uintptr(i)*unsafe.Sizeof(uintptr(0))))
}

func hrErr(r uintptr, what string) error {
	if int32(r) < 0 {
		return fmt.Errorf("%s: HRESULT 0x%08X", what, uint32(r))
	}
	return nil
}

func comRelease(obj uintptr) {
	if obj != 0 {
		syscall.SyscallN(vt(obj, 2), obj)
	}
}

func comInit() { procCoInitializeEx.Call(0, 0) } // COINIT_MULTITHREADED

func setDPIAware() { procSetProcessDpiAwareness.Call(^uintptr(3)) } // PER_MONITOR_AWARE_V2

// dpiScale converte pixels lógicos (100%) para físicos na escala atual do Windows.
func dpiScale(px int) int {
	dpi, _, _ := user32.NewProc("GetDpiForSystem").Call()
	if dpi == 0 {
		dpi = 96
	}
	return px * int(dpi) / 96
}

func messageBox(title, text string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x10)
}

// fitWindow redimensiona a janela para w×h (pixels lógicos), sem passar de 92% da
// área útil do monitor (descontando a barra de tarefas), e a centraliza.
func fitWindow(hwnd uintptr, w, h int) {
	mon, _, _ := procMonitorFromWindow.Call(hwnd, 2) // MONITOR_DEFAULTTONEAREST
	mi := struct {
		size      uint32
		mon, work rect
		flags     uint32
	}{size: 40}
	procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi)))
	aw, ah := int(mi.work.R-mi.work.L), int(mi.work.B-mi.work.T)
	pw, ph := min(dpiScale(w), aw*92/100), min(dpiScale(h), ah*92/100)
	x, y := int(mi.work.L)+(aw-pw)/2, int(mi.work.T)+(ah-ph)/2
	procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), uintptr(pw), uintptr(ph), 0x0004|0x0010) // NOZORDER | NOACTIVATE
}

// openURL abre um link no navegador padrão.
func openURL(url string) {
	verb, _ := windows.UTF16PtrFromString("open")
	target, _ := windows.UTF16PtrFromString(url)
	windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
}

// setWindowIcon usa o ícone embutido no .exe (grupo de ícones #1, gerado pelo
// go-winres a partir de icon.png) na barra de título e na barra de tarefas.
func setWindowIcon(hwnd uintptr) {
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	hinst, _, _ := kernel32.NewProc("GetModuleHandleW").Call(0)
	const iconID = 1 // MAKEINTRESOURCE(1)
	loadImage, sendMessage := user32.NewProc("LoadImageW"), user32.NewProc("SendMessageW")
	getMetrics := user32.NewProc("GetSystemMetricsForDpi")
	dpi, _, _ := user32.NewProc("GetDpiForWindow").Call(hwnd)
	for _, s := range []struct{ which, cx, cy uintptr }{{1, 11, 12}, {0, 49, 50}} { // ICON_BIG (SM_CXICON), ICON_SMALL (SM_CXSMICON)
		cx, _, _ := getMetrics.Call(s.cx, dpi)
		cy, _, _ := getMetrics.Call(s.cy, dpi)
		icon, _, _ := loadImage.Call(hinst, iconID, 1 /* IMAGE_ICON */, cx, cy, 0)
		if icon != 0 {
			sendMessage.Call(hwnd, 0x0080 /* WM_SETICON */, s.which, icon)
		}
	}
}

// watchSizer aumenta a janela para assistir e depois devolve o tamanho que a pessoa
// tinha escolhido — sem nunca brigar com um redimensionamento feito por ela.
type watchSizer struct {
	saved, applied *rect
}

var procIsZoomed = user32.NewProc("IsZoomed")

func windowRect(hwnd uintptr) rect {
	var r rect
	procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return r
}

func (s *watchSizer) enlarge(hwnd uintptr, w, h int) {
	if z, _, _ := procIsZoomed.Call(hwnd); z != 0 || s.saved != nil {
		return // maximizada, ou já aumentada
	}
	cur := windowRect(hwnd)
	if int(cur.R-cur.L) >= dpiScale(w) && int(cur.B-cur.T) >= dpiScale(h)*9/10 {
		return // já é grande o bastante
	}
	fitWindow(hwnd, w, h)
	after := windowRect(hwnd)
	s.saved, s.applied = &cur, &after
}

func (s *watchSizer) restore(hwnd uintptr) {
	saved, applied := s.saved, s.applied
	s.saved, s.applied = nil, nil
	if saved == nil || windowRect(hwnd) != *applied {
		return // a pessoa redimensionou enquanto assistia: respeita
	}
	procSetWindowPos.Call(hwnd, 0, uintptr(saved.L), uintptr(saved.T),
		uintptr(saved.R-saved.L), uintptr(saved.B-saved.T), 0x0004|0x0010)
}

/* ---- Tela cheia (janela sem borda cobrindo o monitor) ---- */

type rect struct{ L, T, R, B int32 }

type fullscreenState struct {
	on    bool
	style uintptr
	rc    rect
}

func setFullscreen(hwnd uintptr, st *fullscreenState, on bool) {
	const gwlStyle = ^uintptr(15) // -16
	const wsOverlappedWindow = 0x00CF0000
	const swpFlags = 0x0020 | 0x0200 | 0x0004 // FRAMECHANGED | NOOWNERZORDER | NOZORDER
	if on == st.on {
		return
	}
	if on {
		st.style, _, _ = procGetWindowLongPtrW.Call(hwnd, gwlStyle)
		procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&st.rc)))
		mon, _, _ := procMonitorFromWindow.Call(hwnd, 2)
		mi := struct {
			size       uint32
			mon, work  rect
			flags      uint32
		}{size: 40}
		procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi)))
		procSetWindowLongPtrW.Call(hwnd, gwlStyle, st.style&^wsOverlappedWindow)
		procSetWindowPos.Call(hwnd, 0, uintptr(mi.mon.L), uintptr(mi.mon.T),
			uintptr(mi.mon.R-mi.mon.L), uintptr(mi.mon.B-mi.mon.T), swpFlags)
	} else {
		procSetWindowLongPtrW.Call(hwnd, gwlStyle, st.style)
		procSetWindowPos.Call(hwnd, 0, uintptr(st.rc.L), uintptr(st.rc.T),
			uintptr(st.rc.R-st.rc.L), uintptr(st.rc.B-st.rc.T), swpFlags)
	}
	st.on = on
}

/* ---- Processos filhos (ffmpeg): sem janela, prioridade alta, morrem junto com o app ---- */

var job windows.Handle

func initJob() {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err == nil {
		job = h
	}
}

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}

func adoptChild(p *os.Process) {
	// acesso total: a API de prioridade de GPU devolve "parâmetro inválido" com menos direitos
	h, err := windows.OpenProcess(windows.PROCESS_ALL_ACCESS, false, uint32(p.Pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	gpuOK := setGPUPriority(h)
	if job != 0 {
		windows.AssignProcessToJobObject(job, h)
	}
	windows.SetPriorityClass(h, windows.HIGH_PRIORITY_CLASS)
	if gpuOK {
		log.Printf("prioridade de GPU elevada (processo %d)", p.Pid)
		return
	}
	go func() { // logo após a criação o processo ainda pode não aceitar: tenta de novo
		for i := 1; i <= 20; i++ {
			time.Sleep(250 * time.Millisecond)
			h, err := windows.OpenProcess(windows.PROCESS_ALL_ACCESS, false, uint32(p.Pid))
			if err != nil {
				return
			}
			ok := setGPUPriority(h)
			windows.CloseHandle(h)
			if ok {
				log.Printf("prioridade de GPU elevada (processo %d, tentativa %d)", p.Pid, i)
				return
			}
		}
		log.Printf("não foi possível elevar a prioridade de GPU (processo %d)", p.Pid)
	}()
}

var procSetGPUPriority = windows.NewLazySystemDLL("gdi32.dll").NewProc("D3DKMTSetProcessSchedulingPriorityClass")

// setGPUPriority dá à captura prioridade no agendador da GPU, para um jogo usando
// 100% da GPU não atrasar a captura dos quadros (mesma técnica do OBS).
// REALTIME só é aceito rodando como administrador; senão cai para HIGH.
func setGPUPriority(h windows.Handle) bool {
	for _, class := range []uintptr{5, 4} { // REALTIME, HIGH
		if r, _, _ := procSetGPUPriority.Call(uintptr(h), class); r == 0 { // STATUS_SUCCESS
			return true
		}
	}
	return false
}
