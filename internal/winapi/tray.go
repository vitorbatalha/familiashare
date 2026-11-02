package winapi

// Ícone na área de notificação (perto do relógio), comportamento do botão de fechar e
// instância única.
//
// O botão de fechar (X, Alt+F4, "Fechar janela" na barra de tarefas) chega como
// WM_CLOSE; a janela é do go-webview2, então ela é "subclassificada": a nossa função
// vê as mensagens primeiro e passa o resto para a original.

import (
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	shell32                    = windows.NewLazySystemDLL("shell32.dll")
	procShellNotifyIconW       = shell32.NewProc("Shell_NotifyIconW")
	procCallWindowProcW        = user32.NewProc("CallWindowProcW")
	procShowWindow             = user32.NewProc("ShowWindow")
	procIsIconic               = user32.NewProc("IsIconic")
	procIsWindowVisible        = user32.NewProc("IsWindowVisible")
	procSetForegroundWindow    = user32.NewProc("SetForegroundWindow")
	procRegisterWindowMessageW = user32.NewProc("RegisterWindowMessageW")
	procCreatePopupMenu        = user32.NewProc("CreatePopupMenu")
	procAppendMenuW            = user32.NewProc("AppendMenuW")
	procTrackPopupMenu         = user32.NewProc("TrackPopupMenu")
	procDestroyMenu            = user32.NewProc("DestroyMenu")
	procGetCursorPos           = user32.NewProc("GetCursorPos")
	procPostMessageW           = user32.NewProc("PostMessageW")
	procGetClassLongPtrW       = user32.NewProc("GetClassLongPtrW")
	procCreateMutexW           = windows.NewLazySystemDLL("kernel32.dll").NewProc("CreateMutexW")
)

const (
	wmClose      = 0x0010
	wmNull       = 0x0000
	wmLButtonUp  = 0x0202
	wmRButtonUp  = 0x0205
	wmTrayIcon   = 0x8000 + 1 // WM_APP+1
	swHide       = 0
	swShow       = 5
	swMinimize   = 6
	swRestore    = 9
	nimAdd       = 0
	nimModify    = 1
	nimDelete    = 2
	nifMessage   = 0x1
	nifIcon      = 0x2
	nifTip       = 0x4
	nifInfo      = 0x10
	hwndBcast    = 0xFFFF
	menuOpen     = 1
	menuQuit     = 2
	tpmReturnCmd = 0x0100
	tpmRightBtn  = 0x0002
)

// notifyIconData é o NOTIFYICONDATAW.
type notifyIconData struct {
	cbSize           uint32
	hWnd             uintptr
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            uintptr
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         windows.GUID
	hBalloonIcon     uintptr
}

// CloseAction é o que fazer quando a pessoa fecha a janela.
type CloseAction int

const (
	CloseQuit     CloseAction = iota // encerra o app
	CloseTray                        // esconde; fica o ícone perto do relógio
	CloseMinimize                    // minimiza na barra de tarefas
)

// Tray cuida do ícone perto do relógio e do botão de fechar.
type Tray struct {
	mu          sync.Mutex
	hwnd        uintptr
	orig        uintptr
	tip         string
	showMsg     uint32 // outra instância pediu para mostrar a janela
	recreateMsg uint32 // o Explorer reiniciou: recolocar o ícone
	onClose     func() CloseAction
	onHidden    func() // chamado ao esconder na bandeja
	quit        func()
}

// NewTray coloca o ícone e passa a tratar o botão de fechar. onClose decide o que
// fazer a cada fechamento; onHidden é chamado quando a janela vai para a bandeja; quit
// encerra o app de verdade (menu "Sair"). key identifica a instância (ver SingleInstance).
func NewTray(hwnd uintptr, tip, key string, onClose func() CloseAction, onHidden, quit func()) *Tray {
	t := &Tray{hwnd: hwnd, tip: tip, onClose: onClose, onHidden: onHidden, quit: quit}
	t.showMsg = registerMsg("familiashare-mostrar-" + key)
	t.recreateMsg = registerMsg("TaskbarCreated")
	t.orig, _, _ = procSetWindowLongPtrW.Call(hwnd, ^uintptr(3) /* GWLP_WNDPROC = -4 */, windows.NewCallback(t.wndProc))
	t.addIcon()
	return t
}

func registerMsg(name string) uint32 {
	p, _ := windows.UTF16PtrFromString(name)
	m, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(p)))
	return uint32(m)
}

func (t *Tray) data() notifyIconData {
	d := notifyIconData{hWnd: t.hwnd, uID: 1}
	d.cbSize = uint32(unsafe.Sizeof(d))
	return d
}

func (t *Tray) addIcon() {
	d := t.data()
	d.uFlags = nifMessage | nifIcon | nifTip
	d.uCallbackMessage = wmTrayIcon
	d.hIcon, _, _ = procGetClassLongPtrW.Call(t.hwnd, ^uintptr(33)) // GCLP_HICONSM = -34
	if d.hIcon == 0 {
		d.hIcon, _, _ = procSendMessageW.Call(t.hwnd, 0x007F /* WM_GETICON */, 0 /* ICON_SMALL */, 0)
	}
	copy(d.szTip[:len(d.szTip)-1], windows.StringToUTF16(t.tip))
	procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&d)))
}

// Remove tira o ícone (ao encerrar o app).
func (t *Tray) Remove() {
	d := t.data()
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&d)))
}

// Notify mostra uma notificação do Windows saindo do ícone.
func (t *Tray) Notify(title, text string) {
	d := t.data()
	d.uFlags = nifInfo
	copy(d.szInfoTitle[:len(d.szInfoTitle)-1], windows.StringToUTF16(title))
	copy(d.szInfo[:len(d.szInfo)-1], windows.StringToUTF16(text))
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&d)))
}

// Show traz a janela de volta (da bandeja ou minimizada) para a frente.
func (t *Tray) Show() {
	procShowWindow.Call(t.hwnd, swShow)
	if ic, _, _ := procIsIconic.Call(t.hwnd); ic != 0 {
		procShowWindow.Call(t.hwnd, swRestore)
	}
	procSetForegroundWindow.Call(t.hwnd)
}

func (t *Tray) wndProc(hwnd, msg, wp, lp uintptr) uintptr {
	switch {
	case msg == wmClose:
		switch t.onClose() {
		case CloseTray:
			procShowWindow.Call(hwnd, swHide)
			if t.onHidden != nil {
				t.onHidden()
			}
			return 0
		case CloseMinimize:
			// Fechar de novo com a janela já minimizada (pela barra de tarefas) encerra:
			// senão não haveria como sair sem o ícone.
			if ic, _, _ := procIsIconic.Call(hwnd); ic == 0 {
				procShowWindow.Call(hwnd, swMinimize)
				return 0
			}
		}
		t.quit()
		return 0
	case msg == wmTrayIcon:
		switch lp & 0xFFFF {
		case wmLButtonUp:
			t.Show()
		case wmRButtonUp:
			t.menu()
		}
		return 0
	case msg != 0 && uint32(msg) == t.showMsg:
		t.Show()
		return 0
	case msg != 0 && uint32(msg) == t.recreateMsg:
		t.addIcon()
	}
	r, _, _ := procCallWindowProcW.Call(t.orig, hwnd, msg, wp, lp)
	return r
}

func (t *Tray) menu() {
	m, _, _ := procCreatePopupMenu.Call()
	defer procDestroyMenu.Call(m)
	open, _ := windows.UTF16PtrFromString("Abrir familiashare")
	quit, _ := windows.UTF16PtrFromString("Sair")
	procAppendMenuW.Call(m, 0, menuOpen, uintptr(unsafe.Pointer(open)))
	procAppendMenuW.Call(m, 0x800 /* MF_SEPARATOR */, 0, 0)
	procAppendMenuW.Call(m, 0, menuQuit, uintptr(unsafe.Pointer(quit)))
	var pt struct{ x, y int32 }
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(t.hwnd) // senão o menu não fecha ao clicar fora
	cmd, _, _ := procTrackPopupMenu.Call(m, tpmReturnCmd|tpmRightBtn, uintptr(pt.x), uintptr(pt.y), 0, t.hwnd, 0)
	procPostMessageW.Call(t.hwnd, wmNull, 0, 0)
	switch cmd {
	case menuOpen:
		t.Show()
	case menuQuit:
		t.quit()
	}
}

// SingleInstance garante um app aberto por pasta de dados (key). Se já houver um,
// pede para ele aparecer e devolve false: quem chamou deve sair. Com o app na bandeja,
// abrir o .exe de novo é o jeito natural de trazê-lo de volta.
func SingleInstance(key string) bool {
	name, _ := windows.UTF16PtrFromString("familiashare-" + key)
	h, _, err := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if h != 0 && err != windows.ERROR_ALREADY_EXISTS {
		instanceMutex = windows.Handle(h) // fica aberto até o processo sair (ou ReleaseInstance)
		return true
	}
	if h != 0 {
		windows.CloseHandle(windows.Handle(h))
	}
	procPostMessageW.Call(hwndBcast, uintptr(registerMsg("familiashare-mostrar-"+key)), 0, 0)
	return false
}

var instanceMutex windows.Handle

// ReleaseInstance libera a vaga de instância única antes de o processo terminar —
// necessário para a versão nova abrir logo depois de uma atualização.
func ReleaseInstance() {
	if instanceMutex != 0 {
		windows.CloseHandle(instanceMutex)
		instanceMutex = 0
	}
}
