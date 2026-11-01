package main

// Funções que a interface (ui/index.html) chama: window.goXxx(...) devolve uma Promise.
// Elas rodam na thread da janela, então nada aqui pode demorar — tudo que usa a
// rede ou processos roda em goroutine.

import (
	"log"
	"sort"
	"strings"
	"sync"

	webview2 "github.com/jchv/go-webview2"
	"github.com/pion/webrtc/v4"
	"github.com/vitorbatalha/familiashare/internal/audio"
	"github.com/vitorbatalha/familiashare/internal/capture"
	"github.com/vitorbatalha/familiashare/internal/config"
	"github.com/vitorbatalha/familiashare/internal/ffmpeg"
	"github.com/vitorbatalha/familiashare/internal/social"
	"github.com/vitorbatalha/familiashare/internal/update"
	"github.com/vitorbatalha/familiashare/internal/winapi"
)

func bind(w webview2.WebView, svc *social.Service, upd *update.Updater) {
	hwnd := uintptr(w.Window())

	// estado e ações sociais
	w.Bind("goState", func() social.State { return svc.State() })
	w.Bind("goLog", func(msg string) { log.Printf("interface: %s", msg) })
	w.Bind("goSetName", func(name string) { go svc.SetName(name) })
	w.Bind("goAddFriend", func(code string) string { return svc.AddFriend(code) })
	w.Bind("goAnswerRequest", func(code string, accept bool) { go svc.AnswerRequest(code, accept) })
	w.Bind("goRemoveFriend", func(code string) { go svc.RemoveFriend(code) })
	w.Bind("goCreateRoom", func(public bool) { go svc.CreateRoom(public) })
	w.Bind("goSetRoomPublic", func(public bool) { go svc.SetRoomPublic(public) })
	w.Bind("goInvite", func(code string) { go svc.Invite(code) })
	w.Bind("goDeclineInvite", func(owner string) { svc.DeclineInvite(owner) })
	w.Bind("goJoin", func(owner string) { go svc.Join(owner) })
	w.Bind("goLeave", func() { go svc.Leave() })

	// transmissão
	w.Bind("goStartShare", func(c config.Config) {
		go func() {
			if err := svc.StartShare(c); err != nil {
				svc.Notice(err.Error())
			}
		}()
	})
	w.Bind("goStopShare", func() { go svc.StopShare() })
	w.Bind("goUpdateShare", func(c config.Config) { go svc.UpdateShare(c) })
	w.Bind("goSignal", func(dst, kind, sdp string, cand *webrtc.ICECandidateInit) {
		go svc.SignalFromUI(dst, kind, sdp, cand)
	})
	w.Bind("goMonitors", func() []capture.Monitor { return capture.ListMonitors() })

	// Extrair o FFmpeg e testar os encoders leva ~1 s: roda em segundo plano.
	var envMu sync.Mutex
	env := map[string]any{"ready": false}
	go func() {
		_, err := ffmpeg.Path()
		avail := ffmpeg.Available()
		fams := map[string]bool{}
		for name := range avail {
			fams[name[strings.LastIndexByte(name, '_')+1:]] = true // "h264_nvenc" → "nvenc"
		}
		var list []string
		for _, f := range ffmpeg.Families {
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

	// apps com som (para sugerir exclusões); a lista é atualizada em segundo plano
	var apps []string
	var appsMu sync.Mutex
	refreshApps := func() {
		go func() {
			a := audio.AppNames()
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

	// janela
	var fs winapi.Fullscreen
	w.Bind("goFullscreen", func(on bool) bool {
		fs.Set(hwnd, on)
		return fs.On()
	})
	var sizer winapi.WatchSizer
	w.Bind("goWatchWindow", func(on bool) {
		if fs.On() {
			return
		}
		if on {
			sizer.Enlarge(hwnd, 1280, 760)
		} else {
			sizer.Restore(hwnd)
		}
	})

	// atualização
	w.Bind("goUpdate", func() update.State { return upd.State() })
	w.Bind("goApplyUpdate", func() { go upd.Apply() })
	w.Bind("goOpenDrive", func() { winapi.OpenURL(update.DriveViewURL) })
}
