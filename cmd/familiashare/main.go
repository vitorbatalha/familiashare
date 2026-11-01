// Comando familiashare: o app de compartilhamento de tela.
//
// Este arquivo só liga as peças (ver docs/ARCHITECTURE.md):
//
//	janela (WebView2) ⇄ bindings.go ⇄ social.Service ⇄ signaling.Node (PeerJS)
//	                                        ↓
//	                                   stream.Host → FFmpeg (captura + encoder) → WebRTC
//
// Variáveis de ambiente para desenvolvimento (ver CONTRIBUTING.md):
//
//	FAMILIASHARE_DATA       pasta de dados (outra identidade, para testar no mesmo PC)
//	FAMILIASHARE_TITLE      título da janela (para distinguir instâncias)
//	FAMILIASHARE_DEBUG      habilita as ferramentas de desenvolvedor do WebView2
//	FAMILIASHARE_BENCH      roda o benchmark de transmissão (ver bench.go)
//	FAMILIASHARE_AUDIOTEST  mede o volume capturado (ver devmodes.go)
package main

import (
	"encoding/json"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"

	webview2 "github.com/jchv/go-webview2"
	"github.com/vitorbatalha/familiashare/internal/config"
	"github.com/vitorbatalha/familiashare/internal/ffmpeg"
	"github.com/vitorbatalha/familiashare/internal/signaling"
	"github.com/vitorbatalha/familiashare/internal/social"
	"github.com/vitorbatalha/familiashare/internal/stream"
	"github.com/vitorbatalha/familiashare/internal/update"
	"github.com/vitorbatalha/familiashare/internal/winapi"
	"github.com/vitorbatalha/familiashare/ui"
)

const appName = "familiashare"

func main() {
	winapi.SetDPIAware()
	dataDir := filepath.Join(os.Getenv("LOCALAPPDATA"), "familiashare")
	if d := os.Getenv("FAMILIASHARE_DATA"); d != "" {
		dataDir = d
	}
	os.MkdirAll(dataDir, 0o755)
	migrateOldData(dataDir)
	if f, err := os.Create(filepath.Join(dataDir, "familiashare.log")); err == nil {
		log.SetOutput(f)
		defer f.Close()
	}
	if f, err := os.OpenFile(filepath.Join(dataDir, "crash.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		debug.SetCrashOutput(f, debug.CrashOptions{}) // se o app fechar sozinho, o motivo fica aqui
	}
	winapi.InitJob()
	ffmpeg.Init(dataDir)

	if os.Getenv("FAMILIASHARE_AUDIOTEST") != "" {
		audioTest()
		return
	}
	if s, err := strconv.Atoi(os.Getenv("FAMILIASHARE_BENCH")); err == nil && s > 0 {
		runBench(dataDir, s)
		return
	}

	// A interface é servida localmente (o WebView2 precisa de uma origem http).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		winapi.MessageBox(appName, "Erro ao iniciar: "+err.Error())
		return
	}
	go http.Serve(ln, http.FileServer(http.FS(fs.FS(ui.Files))))

	// o som de quem assiste começa sem precisar clicar em "ativar som"
	os.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", "--autoplay-policy=no-user-gesture-required")
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     os.Getenv("FAMILIASHARE_DEBUG") != "",
		AutoFocus: true,
		DataPath:  filepath.Join(dataDir, "webview"),
		WindowOptions: webview2.WindowOptions{
			Title: appName, Width: uint(winapi.DPIScale(500)), Height: uint(winapi.DPIScale(860)), Center: true,
		},
	})
	if w == nil {
		winapi.MessageBox(appName, "Não foi possível abrir a janela. Instale o Microsoft Edge WebView2 Runtime:\nhttps://go.microsoft.com/fwlink/p/?LinkId=2124703")
		return
	}
	defer w.Destroy()
	hwnd := uintptr(w.Window())
	w.SetSize(winapi.DPIScale(380), winapi.DPIScale(520), webview2.HintMin) // abaixo disso a interface não cabe
	winapi.FitWindow(hwnd, 500, 860)
	winapi.SetWindowIcon(hwnd)
	if t := os.Getenv("FAMILIASHARE_TITLE"); t != "" {
		w.SetTitle(t)
	}

	cfg := config.Load(dataDir)
	config.Save(dataDir, cfg)         // fixa o código de amigo gerado na primeira execução
	toUI := func(ev map[string]any) { // eventos de WebRTC para quem assiste (JS)
		b, _ := json.Marshal(ev)
		w.Dispatch(func() { w.Eval("window.onSignal && onSignal(" + string(b) + ")") })
	}
	save := func(c config.Config) { config.Save(dataDir, c) }
	svc := social.New(cfg, save, stream.New(), toUI)
	svc.Run(signaling.Start(cfg.MyCode, svc.OnMessage, svc.OnConnected))
	upd := update.New(func() { w.Dispatch(w.Terminate) })

	bind(w, svc, upd)
	w.Navigate("http://" + ln.Addr().String() + "/")
	w.Run()
	svc.Shutdown()
	upd.RestartIfNeeded()
}

// migrateOldData traz amigos, código e configurações da época em que o app se chamava "tela".
func migrateOldData(dataDir string) {
	if _, err := os.Stat(config.Path(dataDir)); err == nil {
		return
	}
	old := filepath.Join(os.Getenv("LOCALAPPDATA"), "tela", "config.json")
	if b, err := os.ReadFile(old); err == nil {
		os.WriteFile(config.Path(dataDir), b, 0o644)
	}
}
