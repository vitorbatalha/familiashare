package winapi

// Janela do app: escala de DPI, tamanho, ícone, tela cheia, abrir links.

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                     = windows.NewLazySystemDLL("user32.dll")
	procMessageBoxW            = user32.NewProc("MessageBoxW")
	procSetProcessDpiAwareness = user32.NewProc("SetProcessDpiAwarenessContext")
	procGetDpiForSystem        = user32.NewProc("GetDpiForSystem")
	procGetDpiForWindow        = user32.NewProc("GetDpiForWindow")
	procGetSystemMetricsForDpi = user32.NewProc("GetSystemMetricsForDpi")
	procGetWindowLongPtrW      = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtrW      = user32.NewProc("SetWindowLongPtrW")
	procGetWindowRect          = user32.NewProc("GetWindowRect")
	procSetWindowPos           = user32.NewProc("SetWindowPos")
	procMonitorFromWindow      = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW        = user32.NewProc("GetMonitorInfoW")
	procIsZoomed               = user32.NewProc("IsZoomed")
	procLoadImageW             = user32.NewProc("LoadImageW")
	procSendMessageW           = user32.NewProc("SendMessageW")
	procGetModuleHandleW       = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW")
)

// Rect é um RECT do Windows (pixels físicos).
type Rect struct{ L, T, R, B int32 }

// SetDPIAware deixa o processo ciente de DPI por monitor (V2). Chamar antes de criar janelas.
func SetDPIAware() { procSetProcessDpiAwareness.Call(^uintptr(3)) } // PER_MONITOR_AWARE_V2

// DPIScale converte pixels lógicos (100%) para físicos na escala atual do Windows.
func DPIScale(px int) int {
	dpi, _, _ := procGetDpiForSystem.Call()
	if dpi == 0 {
		dpi = 96
	}
	return px * int(dpi) / 96
}

// MessageBox mostra uma caixa de erro simples (para falhas antes de existir janela).
func MessageBox(title, text string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x10)
}

// OpenURL abre um link no navegador padrão.
func OpenURL(url string) {
	verb, _ := windows.UTF16PtrFromString("open")
	target, _ := windows.UTF16PtrFromString(url)
	windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
}

type monitorInfo struct {
	size      uint32
	mon, work Rect
	flags     uint32
}

func monitorOf(hwnd uintptr) monitorInfo {
	mon, _, _ := procMonitorFromWindow.Call(hwnd, 2) // MONITOR_DEFAULTTONEAREST
	mi := monitorInfo{size: 40}
	procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi)))
	return mi
}

// WindowRect devolve a posição e o tamanho atuais da janela.
func WindowRect(hwnd uintptr) Rect {
	var r Rect
	procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return r
}

// FitWindow redimensiona a janela para w×h (pixels lógicos), sem passar de 92% da
// área útil do monitor (descontando a barra de tarefas), e a centraliza.
func FitWindow(hwnd uintptr, w, h int) {
	mi := monitorOf(hwnd)
	aw, ah := int(mi.work.R-mi.work.L), int(mi.work.B-mi.work.T)
	pw, ph := min(DPIScale(w), aw*92/100), min(DPIScale(h), ah*92/100)
	x, y := int(mi.work.L)+(aw-pw)/2, int(mi.work.T)+(ah-ph)/2
	procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), uintptr(pw), uintptr(ph), 0x0004|0x0010) // NOZORDER | NOACTIVATE
}

// SetWindowIcon usa o ícone embutido no .exe (grupo de ícones #1, gerado pelo
// go-winres a partir de assets/icon.png) na barra de título e na barra de tarefas.
func SetWindowIcon(hwnd uintptr) {
	hinst, _, _ := procGetModuleHandleW.Call(0)
	const iconID = 1 // MAKEINTRESOURCE(1)
	dpi, _, _ := procGetDpiForWindow.Call(hwnd)
	for _, s := range []struct{ which, cx, cy uintptr }{{1, 11, 12}, {0, 49, 50}} { // ICON_BIG (SM_CXICON), ICON_SMALL (SM_CXSMICON)
		cx, _, _ := procGetSystemMetricsForDpi.Call(s.cx, dpi)
		cy, _, _ := procGetSystemMetricsForDpi.Call(s.cy, dpi)
		icon, _, _ := procLoadImageW.Call(hinst, iconID, 1 /* IMAGE_ICON */, cx, cy, 0)
		if icon != 0 {
			procSendMessageW.Call(hwnd, 0x0080 /* WM_SETICON */, s.which, icon)
		}
	}
}

// WatchSizer aumenta a janela para assistir e depois devolve o tamanho que a pessoa
// tinha escolhido — sem nunca brigar com um redimensionamento feito por ela.
type WatchSizer struct {
	saved, applied *Rect
}

// Enlarge aumenta a janela para w×h (lógicos), se ela for menor e não estiver maximizada.
func (s *WatchSizer) Enlarge(hwnd uintptr, w, h int) {
	if z, _, _ := procIsZoomed.Call(hwnd); z != 0 || s.saved != nil {
		return // maximizada, ou já aumentada
	}
	cur := WindowRect(hwnd)
	if int(cur.R-cur.L) >= DPIScale(w) && int(cur.B-cur.T) >= DPIScale(h)*9/10 {
		return // já é grande o bastante
	}
	FitWindow(hwnd, w, h)
	after := WindowRect(hwnd)
	s.saved, s.applied = &cur, &after
}

// Restore volta ao tamanho anterior, a menos que a pessoa tenha redimensionado no meio-tempo.
func (s *WatchSizer) Restore(hwnd uintptr) {
	saved, applied := s.saved, s.applied
	s.saved, s.applied = nil, nil
	if saved == nil || WindowRect(hwnd) != *applied {
		return // a pessoa redimensionou enquanto assistia: respeita
	}
	procSetWindowPos.Call(hwnd, 0, uintptr(saved.L), uintptr(saved.T),
		uintptr(saved.R-saved.L), uintptr(saved.B-saved.T), 0x0004|0x0010)
}

// Fullscreen deixa a janela sem borda cobrindo o monitor inteiro, e desfaz.
type Fullscreen struct {
	on    bool
	style uintptr
	rc    Rect
}

func (f *Fullscreen) On() bool { return f.on }

// Set liga ou desliga a tela cheia.
func (f *Fullscreen) Set(hwnd uintptr, on bool) {
	const gwlStyle = ^uintptr(15) // -16
	const wsOverlappedWindow = 0x00CF0000
	const swpFlags = 0x0020 | 0x0200 | 0x0004 // FRAMECHANGED | NOOWNERZORDER | NOZORDER
	if on == f.on {
		return
	}
	if on {
		f.style, _, _ = procGetWindowLongPtrW.Call(hwnd, gwlStyle)
		f.rc = WindowRect(hwnd)
		mi := monitorOf(hwnd)
		procSetWindowLongPtrW.Call(hwnd, gwlStyle, f.style&^wsOverlappedWindow)
		procSetWindowPos.Call(hwnd, 0, uintptr(mi.mon.L), uintptr(mi.mon.T),
			uintptr(mi.mon.R-mi.mon.L), uintptr(mi.mon.B-mi.mon.T), swpFlags)
	} else {
		procSetWindowLongPtrW.Call(hwnd, gwlStyle, f.style)
		procSetWindowPos.Call(hwnd, 0, uintptr(f.rc.L), uintptr(f.rc.T),
			uintptr(f.rc.R-f.rc.L), uintptr(f.rc.B-f.rc.T), swpFlags)
	}
	f.on = on
}
