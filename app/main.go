package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	webview2 "github.com/jchv/go-webview2"
	"github.com/pion/webrtc/v4"
)

//go:embed ui
var uiFiles embed.FS

var dataDir string

func main() {
	setDPIAware()
	dataDir = filepath.Join(os.Getenv("LOCALAPPDATA"), "familiashare")
	if d := os.Getenv("TELA_DATA"); d != "" { // outra identidade (para testes no mesmo PC)
		dataDir = d
	}
	os.MkdirAll(dataDir, 0o755)
	migrateOldData()
	if f, err := os.Create(filepath.Join(dataDir, "tela.log")); err == nil {
		log.SetOutput(f)
		defer f.Close()
	}
	if f, err := os.OpenFile(filepath.Join(dataDir, "crash.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		debug.SetCrashOutput(f, debug.CrashOptions{}) // se o app fechar sozinho, o motivo fica aqui
	}
	initJob()

	if os.Getenv("TELA_AUDIOTEST") != "" { // modo de teste: mede o volume capturado
		audioTest()
		return
	}
	if s, err := strconv.Atoi(os.Getenv("FAMILIASHARE_BENCH")); err == nil && s > 0 { // ver bench.go
		runBench(s)
		return
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		messageBox(appName, "Erro ao iniciar: "+err.Error())
		return
	}
	sub, _ := fs.Sub(uiFiles, "ui")
	go http.Serve(ln, http.FileServer(http.FS(sub)))

	// o som de quem assiste começa sem precisar clicar em "ativar som"
	os.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS", "--autoplay-policy=no-user-gesture-required")
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     os.Getenv("TELA_DEBUG") != "",
		AutoFocus: true,
		DataPath:  filepath.Join(dataDir, "webview"),
		WindowOptions: webview2.WindowOptions{
			Title: appName, Width: uint(dpiScale(500)), Height: uint(dpiScale(860)), Center: true,
		},
	})
	if w == nil {
		messageBox(appName, "Não foi possível abrir a janela. Instale o Microsoft Edge WebView2 Runtime:\nhttps://go.microsoft.com/fwlink/p/?LinkId=2124703")
		return
	}
	defer w.Destroy()
	w.SetSize(dpiScale(380), dpiScale(520), webview2.HintMin) // abaixo disso a interface não cabe
	fitWindow(uintptr(w.Window()), 500, 860)
	setWindowIcon(uintptr(w.Window()))
	if t := os.Getenv("TELA_TITLE"); t != "" {
		w.SetTitle(t)
	}

	cfg := loadConfig()
	saveConfig(cfg) // fixa o código de amigo gerado na primeira execução
	app := newApp(cfg, func(ev map[string]any) {
		b, _ := json.Marshal(ev)
		w.Dispatch(func() { w.Eval("window.onSignal && onSignal(" + string(b) + ")") })
	})
	upd := newUpdater(func() { w.Dispatch(w.Terminate) })
	bind(w, app)
	w.Bind("goUpdate", func() UpdateState { return upd.State() })
	w.Bind("goApplyUpdate", func() { go upd.Apply() })
	w.Bind("goOpenDrive", func() { openURL(driveViewURL) })
	w.Navigate("http://" + ln.Addr().String() + "/")
	w.Run()
	app.Shutdown()
	upd.RestartIfNeeded()
}

// As funções ligadas rodam na thread da janela: nada aqui pode demorar
// (por isso tudo que usa a rede roda em goroutine).
func bind(w webview2.WebView, app *App) {
	w.Bind("goState", func() State { return app.State() })
	w.Bind("goLog", func(msg string) { log.Printf("interface: %s", msg) })
	w.Bind("goSetName", func(name string) { go app.SetName(name) })
	w.Bind("goAddFriend", func(code string) string { return app.AddFriend(code) })
	w.Bind("goAnswerRequest", func(code string, accept bool) { go app.AnswerRequest(code, accept) })
	w.Bind("goRemoveFriend", func(code string) { go app.RemoveFriend(code) })
	w.Bind("goCreateRoom", func(public bool) { go app.CreateRoom(public) })
	w.Bind("goSetRoomPublic", func(public bool) { go app.SetRoomPublic(public) })
	w.Bind("goInvite", func(code string) { go app.Invite(code) })
	w.Bind("goDeclineInvite", func(owner string) { app.DeclineInvite(owner) })
	w.Bind("goJoin", func(owner string) { go app.Join(owner) })
	w.Bind("goLeave", func() { go app.Leave() })
	w.Bind("goStartShare", func(c Config) {
		go func() {
			if err := app.StartShare(c); err != nil {
				app.Notice(err.Error())
			}
		}()
	})
	w.Bind("goStopShare", func() { go app.StopShare() })
	w.Bind("goUpdateShare", func(c Config) { go app.UpdateShare(c) })
	w.Bind("goSignal", func(dst, kind, sdp string, cand *webrtc.ICECandidateInit) {
		go app.SignalFromUI(dst, kind, sdp, cand)
	})
	w.Bind("goMonitors", func() []Monitor { return listMonitors() })

	// Detectar encoders e listar apps com áudio leva ~1 s: roda em segundo plano.
	var envMu sync.Mutex
	env := map[string]any{"ready": false}
	go func() {
		_, err := ffmpegPath()
		avail := availableEncoders()
		fams := map[string]bool{}
		for name := range avail {
			fams[name[strings.LastIndexByte(name, '_')+1:]] = true // "h264_nvenc" → "nvenc"
		}
		var list []string
		for _, f := range encoderFamilies {
			if fams[f] {
				list = append(list, f)
			}
		}
		envMu.Lock()
		env = map[string]any{"ready": true, "encoders": list, "codecs": avail}
		if err != nil {
			env["err"] = err.Error()
		}
		envMu.Unlock()
	}()
	w.Bind("goEnv", func() map[string]any {
		envMu.Lock()
		defer envMu.Unlock()
		return env
	})

	var apps []string
	var appsMu sync.Mutex
	refreshApps := func() {
		go func() {
			a := audioAppNames()
			appsMu.Lock()
			apps = a
			appsMu.Unlock()
		}()
	}
	refreshApps()
	w.Bind("goAudioApps", func() []string {
		appsMu.Lock()
		defer appsMu.Unlock()
		refreshApps()
		out := append([]string(nil), apps...)
		sort.Strings(out)
		return out
	})

	var fs fullscreenState
	w.Bind("goFullscreen", func(on bool) bool {
		setFullscreen(uintptr(w.Window()), &fs, on)
		return fs.on
	})
	var sizer watchSizer
	w.Bind("goWatchWindow", func(on bool) {
		if fs.on {
			return
		}
		if on {
			sizer.enlarge(uintptr(w.Window()), 1280, 760)
		} else {
			sizer.restore(uintptr(w.Window()))
		}
	})
}

const appName = "familiashare"

// migrateOldData traz amigos, código e configurações da época em que o app se chamava "tela".
func migrateOldData() {
	if _, err := os.Stat(configPath()); err == nil {
		return
	}
	old := filepath.Join(os.Getenv("LOCALAPPDATA"), "tela", "config.json")
	if b, err := os.ReadFile(old); err == nil {
		os.WriteFile(configPath(), b, 0o644)
	}
}

func audioTest() {
	log.SetOutput(os.Stdout)
	var excl []string
	if e := os.Getenv("TELA_EXCLUDE"); e != "" {
		excl = strings.Split(e, ",")
	}
	eng := newAudioEngine(excl)
	var sum float64
	var n int
	last := time.Now()
	eng.Run(func(s []float32) error {
		for _, v := range s {
			sum += float64(v) * float64(v)
		}
		n += len(s)
		if time.Since(last) >= time.Second {
			log.Printf("rms=%.4f modo=%q", math.Sqrt(sum/float64(n)), eng.Mode())
			sum, n, last = 0, 0, time.Now()
		}
		return nil
	})
}
