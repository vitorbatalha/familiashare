package main

// Benchmark de transmissão: o app transmite de verdade (captura → FFmpeg → WebRTC) para
// um espectador interno, no mesmo processo, sem rede externa. Serve para garantir que
// mudanças no código não pioram qualidade nem desempenho.
//
// Uso (PowerShell):
//   $env:FAMILIASHARE_BENCH = "20"            # segundos medidos (após 3 s de aquecimento)
//   $env:FAMILIASHARE_BENCH_ENCODER = "nvenc" # opcional: auto | nvenc | amf | qsv | mf
//   $env:FAMILIASHARE_BENCH_CODEC = "h264"    # opcional: h264 | av1
//   $env:FAMILIASHARE_BENCH_MBPS = "12"       # opcional
//   .\familiashare.exe | Out-String           # imprime uma linha JSON

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"
	"unsafe"

	"github.com/pion/webrtc/v4"
	"golang.org/x/sys/windows"
)

type BenchResult struct {
	Encoder   string  `json:"encoder"`
	Codec     string  `json:"codec"`
	Pipeline  string  `json:"pipeline"`
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	Seconds   float64 `json:"seconds"`
	SendFPS   float64 `json:"send_fps"`   // quadros enviados por segundo
	RecvFPS   float64 `json:"recv_fps"`   // quadros completos recebidos por segundo
	RecvMbps  float64 `json:"recv_mbps"`  // bitrate de vídeo recebido
	LossPct   float64 `json:"loss_pct"`   // pacotes de vídeo que nunca chegaram
	GapP50ms  float64 `json:"gap_p50_ms"` // intervalo entre quadros (mediana)
	GapP95ms  float64 `json:"gap_p95_ms"` // engasgos aparecem aqui
	GapMaxms  float64 `json:"gap_max_ms"`
	AudioKbps float64 `json:"audio_kbps"`
	CPUApp    float64 `json:"cpu_app_pct"`    // % de um núcleo: o próprio app
	CPUFFmpeg float64 `json:"cpu_ffmpeg_pct"` // % de um núcleo: processos FFmpeg
	Err       string  `json:"err,omitempty"`
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func runBench(secs int) {
	cfg := loadConfig()
	cfg.Encoder = envOr("FAMILIASHARE_BENCH_ENCODER", "auto")
	cfg.Codec = envOr("FAMILIASHARE_BENCH_CODEC", "h264")
	if m, err := strconv.Atoi(os.Getenv("FAMILIASHARE_BENCH_MBPS")); err == nil {
		cfg.Mbps = m
	}
	cfg.Exclude = nil
	res := BenchResult{Encoder: cfg.Encoder, Codec: cfg.Codec}
	out := func() {
		b, _ := json.Marshal(res)
		fmt.Println(string(b))
	}

	h := newHost()
	sig := newLoopSignaler()
	if err := h.Start(cfg, sig); err != nil {
		res.Err = err.Error()
		out()
		return
	}
	defer h.Stop()
	sig.host = h

	// espera o pipeline ficar ao vivo
	deadline := time.Now().Add(40 * time.Second)
	for h.Stats().State != "live" {
		if time.Now().After(deadline) {
			res.Err = "transmissão não iniciou: " + h.Stats().Err
			out()
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	h.HandleWatch("bench")
	time.Sleep(3 * time.Second) // aquecimento: conexão, primeiro quadro-chave, controle de banda

	rx := sig.rx
	rx.reset()
	sentFrames0 := h.video.frames.Load()
	app0, ff0 := cpuTimes()
	t0 := time.Now()
	time.Sleep(time.Duration(secs) * time.Second)
	el := time.Since(t0).Seconds()
	app1, ff1 := cpuTimes()

	st := h.Stats()
	res.Pipeline, res.Width, res.Height = st.Pipeline, st.Width, st.Height
	res.Seconds = el
	res.SendFPS = float64(h.video.frames.Load()-sentFrames0) / el
	res.CPUApp = (app1 - app0).Seconds() / el * 100
	res.CPUFFmpeg = (ff1 - ff0).Seconds() / el * 100
	rx.fill(&res, el)
	out()
}

/* ---------- sinalização em laço local ---------- */

type loopSignaler struct {
	host *Host
	rx   *benchReceiver
	q    chan func()
}

func newLoopSignaler() *loopSignaler {
	s := &loopSignaler{rx: &benchReceiver{}, q: make(chan func(), 256)}
	go func() { // processa tudo em ordem, como a sinalização real
		for f := range s.q {
			f()
		}
	}()
	return s
}

func (s *loopSignaler) Me() string                   { return "bench-host" }
func (s *loopSignaler) Send(dst, t string, d any) error { return nil }
func (s *loopSignaler) SendOffer(dst, sdp string) error {
	s.q <- func() { s.rx.onOffer(s, sdp) }
	return nil
}
func (s *loopSignaler) SendCandidate(dst, owner string, c webrtc.ICECandidateInit) error {
	s.q <- func() {
		if s.rx.pc != nil {
			s.rx.pc.AddICECandidate(c)
		}
	}
	return nil
}

/* ---------- espectador interno: conta o que chega ---------- */

type benchReceiver struct {
	pc *webrtc.PeerConnection

	mu         sync.Mutex
	vBytes     int64
	vPackets   int64
	vLost      int64
	frames     int64
	lastSeq    uint16
	haveSeq    bool
	lastFrame  time.Time
	gaps       []float64
	aBytes     int64
}

func (r *benchReceiver) onOffer(s *loopSignaler, sdp string) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{}) // codecs e NACK padrão, como um navegador
	if err != nil {
		return
	}
	r.pc = pc
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			s.host.HandleCandidate("bench", c.ToJSON())
		}
	})
	pc.OnTrack(func(t *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		video := t.Kind() == webrtc.RTPCodecTypeVideo
		for {
			p, _, err := t.ReadRTP()
			if err != nil {
				return
			}
			now := time.Now()
			r.mu.Lock()
			if !video {
				r.aBytes += int64(len(p.Payload))
				r.mu.Unlock()
				continue
			}
			r.vBytes += int64(len(p.Payload) + 12)
			r.vPackets++
			if r.haveSeq {
				if d := p.SequenceNumber - r.lastSeq; d > 1 && d < 3000 {
					r.vLost += int64(d - 1)
				}
			}
			if !r.haveSeq || p.SequenceNumber-r.lastSeq < 32768 { // ignora retransmissões atrasadas
				r.lastSeq, r.haveSeq = p.SequenceNumber, true
			}
			if p.Marker {
				r.frames++
				if !r.lastFrame.IsZero() {
					r.gaps = append(r.gaps, float64(now.Sub(r.lastFrame).Microseconds())/1000)
				}
				r.lastFrame = now
			}
			r.mu.Unlock()
		}
	})
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp}); err != nil {
		return
	}
	ans, err := pc.CreateAnswer(nil)
	if err != nil {
		return
	}
	pc.SetLocalDescription(ans)
	s.host.HandleAnswer("bench", ans.SDP)
}

func (r *benchReceiver) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.vBytes, r.vPackets, r.vLost, r.frames, r.aBytes = 0, 0, 0, 0, 0
	r.gaps = nil
	r.lastFrame = time.Time{}
}

func (r *benchReceiver) fill(res *BenchResult, el float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	res.RecvFPS = float64(r.frames) / el
	res.RecvMbps = float64(r.vBytes) * 8 / 1e6 / el
	res.AudioKbps = float64(r.aBytes) * 8 / 1e3 / el
	if total := r.vPackets + r.vLost; total > 0 {
		res.LossPct = float64(r.vLost) / float64(total) * 100
	}
	if len(r.gaps) > 0 {
		g := append([]float64(nil), r.gaps...)
		sort.Float64s(g)
		res.GapP50ms = g[len(g)/2]
		res.GapP95ms = g[len(g)*95/100]
		res.GapMaxms = g[len(g)-1]
	}
}

/* ---------- uso de CPU: o app e os FFmpeg (todos no job object) ---------- */

type jobAccounting struct {
	TotalUserTime, TotalKernelTime                      int64
	ThisPeriodTotalUserTime, ThisPeriodTotalKernelTime  int64
	TotalPageFaultCount, TotalProcesses                 uint32
	ActiveProcesses, TotalTerminatedProcesses           uint32
}

func cpuTimes() (app, ffmpeg time.Duration) {
	var c, e, k, u windows.Filetime
	if windows.GetProcessTimes(windows.CurrentProcess(), &c, &e, &k, &u) == nil {
		app = time.Duration((int64(k.HighDateTime)<<32|int64(k.LowDateTime))+(int64(u.HighDateTime)<<32|int64(u.LowDateTime))) * 100
	}
	if job != 0 {
		var info jobAccounting
		if windows.QueryInformationJobObject(job, 1 /* JobObjectBasicAccountingInformation */, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil) == nil {
			ffmpeg = time.Duration(info.TotalUserTime+info.TotalKernelTime) * 100
		}
	}
	return app, ffmpeg
}
