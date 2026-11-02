package winapi

// Barra de título própria: a página desenha o nome e os botões; aqui some a barra do
// Windows e ficam as bordas de redimensionar, a sombra e o encaixe (arrastar para o canto,
// Win+setas).
//
// A janela continua com o estilo normal (WS_OVERLAPPEDWINDOW) — é isso que mantém a
// sombra, as animações e o encaixe. Só a área da barra vira área da página: no
// WM_NCCALCSIZE o topo volta para onde a janela começa. As bordas de baixo e dos lados
// são as do Windows (invisíveis, por fora da janela); a de cima fica por cima da página,
// então ela avisa pelo goResizeTop (ver StartResize).

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procReleaseCapture        = user32.NewProc("ReleaseCapture")
	procGetSystemMenu         = user32.NewProc("GetSystemMenu")
	procEnableMenuItem        = user32.NewProc("EnableMenuItem")
	procDwmSetWindowAttribute = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")
)

const (
	wmNcCalcSize    = 0x0083
	wmNcLButtonDown = 0x00A1
	wmSysCommand    = 0x0112
	wsCaption       = 0x00C00000
	htCaption       = 2
	htTop           = 12
	htTopLeft       = 13
	htTopRight      = 14
	swMaximize      = 3
)

// Frame cuida da janela sem a barra do Windows.
type Frame struct {
	hwnd, orig uintptr
}

// NewFrame tira a barra de título do Windows (a página desenha a dela).
func NewFrame(hwnd uintptr) *Frame {
	f := &Frame{hwnd: hwnd}
	f.orig, _, _ = procSetWindowLongPtrW.Call(hwnd, ^uintptr(3) /* GWLP_WNDPROC */, windows.NewCallback(f.wndProc))
	// Windows 11: modo escuro na borda e no menu da janela, e borda da cor da página
	dark := uint32(1)
	procDwmSetWindowAttribute.Call(hwnd, 20 /* DWMWA_USE_IMMERSIVE_DARK_MODE */, uintptr(unsafe.Pointer(&dark)), 4)
	border := uint32(0x2a2626) // #26262a em COLORREF (0x00BBGGRR)
	procDwmSetWindowAttribute.Call(hwnd, 34 /* DWMWA_BORDER_COLOR */, uintptr(unsafe.Pointer(&border)), 4)
	// recalcula a área da janela já sem a barra
	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, 0x0020|0x0002|0x0001|0x0004|0x0010) // FRAMECHANGED|NOMOVE|NOSIZE|NOZORDER|NOACTIVATE
	return f
}

func (f *Frame) wndProc(hwnd, msg, wp, lp uintptr) uintptr {
	if msg == wmNcCalcSize && wp != 0 {
		style, _, _ := procGetWindowLongPtrW.Call(hwnd, ^uintptr(15) /* GWL_STYLE */)
		if style&wsCaption != 0 { // tela cheia tira o estilo: aí fica o cálculo normal
			rc := (*Rect)(unsafe.Pointer(lp)) // primeiro RECT de NCCALCSIZE_PARAMS
			top := rc.T
			procCallWindowProcW.Call(f.orig, hwnd, msg, wp, lp) // bordas normais dos lados e de baixo
			rc.T = top
			if f.Maximized() {
				// maximizada, a janela passa da tela pela espessura da borda: sem
				// compensar, o topo da página ficaria cortado
				dpi, _, _ := procGetDpiForWindow.Call(hwnd)
				fy, _, _ := procGetSystemMetricsForDpi.Call(33 /* SM_CYSIZEFRAME */, dpi)
				pad, _, _ := procGetSystemMetricsForDpi.Call(92 /* SM_CXPADDEDBORDER */, dpi)
				rc.T += int32(fy + pad)
			}
			return 0
		}
	}
	r, _, _ := procCallWindowProcW.Call(f.orig, hwnd, msg, wp, lp)
	return r
}

// Maximized diz se a janela está maximizada.
func (f *Frame) Maximized() bool {
	z, _, _ := procIsZoomed.Call(f.hwnd)
	return z != 0
}

// Minimize minimiza a janela.
func (f *Frame) Minimize() { procShowWindow.Call(f.hwnd, swMinimize) }

// ToggleMaximize maximiza ou volta ao tamanho anterior.
func (f *Frame) ToggleMaximize() {
	if f.Maximized() {
		procShowWindow.Call(f.hwnd, swRestore)
	} else {
		procShowWindow.Call(f.hwnd, swMaximize)
	}
}

// Close faz o mesmo que o X do Windows (WM_CLOSE): respeita "Quando você fecha a janela".
func (f *Frame) Close() { procPostMessageW.Call(f.hwnd, wmClose, 0, 0) }

// StartDrag começa a arrastar a janela, como se a pessoa tivesse segurado a barra do
// Windows (assim o encaixe nos cantos funciona). Chamar com o botão do mouse apertado.
func (f *Frame) StartDrag() { f.ncDown(htCaption) }

// StartResize começa a redimensionar pela borda de cima: edge é "top", "left" (canto
// de cima à esquerda) ou "right".
func (f *Frame) StartResize(edge string) {
	if f.Maximized() {
		return
	}
	ht := map[string]uintptr{"left": htTopLeft, "right": htTopRight}[edge]
	if ht == 0 {
		ht = htTop
	}
	f.ncDown(ht)
}

func (f *Frame) ncDown(ht uintptr) {
	var pt struct{ x, y int32 }
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procReleaseCapture.Call()
	procPostMessageW.Call(f.hwnd, wmNcLButtonDown, ht, uintptr(uint16(pt.x))|uintptr(uint16(pt.y))<<16)
}

// SystemMenu abre o menu da janela (Restaurar, Mover, Minimizar…) onde está o mouse,
// como o clique direito na barra do Windows.
func (f *Frame) SystemMenu() {
	m, _, _ := procGetSystemMenu.Call(f.hwnd, 0)
	if m == 0 {
		return
	}
	const byCmd, enabled, grayed = 0, 0, 1
	maxed := f.Maximized()
	set := func(cmd uintptr, on bool) {
		flag := uintptr(grayed)
		if on {
			flag = enabled
		}
		procEnableMenuItem.Call(m, cmd, byCmd|flag)
	}
	set(0xF120 /* SC_RESTORE */, maxed)
	set(0xF010 /* SC_MOVE */, !maxed)
	set(0xF000 /* SC_SIZE */, !maxed)
	set(0xF030 /* SC_MAXIMIZE */, !maxed)
	var pt struct{ x, y int32 }
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	cmd, _, _ := procTrackPopupMenu.Call(m, tpmReturnCmd|tpmRightBtn, uintptr(pt.x), uintptr(pt.y), 0, f.hwnd, 0)
	if cmd != 0 {
		procPostMessageW.Call(f.hwnd, wmSysCommand, cmd, 0)
	}
}
