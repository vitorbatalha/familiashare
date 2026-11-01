package main

import (
	"bufio"
	"context"
	"errors"
	"log"
	"math"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/nack"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// kbps: bitrate escolhido, ou automático proporcional a pixels × fps (1080p60 ≈ 25 Mbps).
func (c Config) kbps(mon Monitor) int {
	if c.Mbps > 0 {
		return c.Mbps * 1000
	}
	w, h := mon.Width, mon.Height
	if c.Height > 0 && c.Height < h {
		w, h = w*c.Height/h, c.Height
	}
	k := float64(w*h*c.FPS) * 0.2 / 1000
	if c.Codec == "av1" {
		k *= 0.75
	}
	return int(math.Max(8000, math.Min(60000, math.Round(k/1000)*1000)))
}

var iceServers = []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302", "stun:stun.cloudflare.com:3478"}}}

/* ---------- Encaminhamento RTP do FFmpeg para a trilha WebRTC ---------- */

type forwarder struct {
	track    *webrtc.TrackLocalStaticRTP
	clock    float64
	t0       time.Time
	seq      uint16
	newSess  bool
	srcBase  uint32
	base     uint32
	pace     *pacer
	bytes    atomic.Uint64
	frames   atomic.Uint64
}

// Cada reinício do FFmpeg gera SSRC/seq/timestamps novos; aqui tudo vira uma sequência contínua.
func (f *forwarder) write(p *rtp.Packet) {
	if f.newSess {
		f.srcBase = p.Timestamp
		f.base = uint32(time.Since(f.t0).Seconds() * f.clock)
		f.newSess = false
	}
	p.Timestamp = f.base + (p.Timestamp - f.srcBase)
	p.SequenceNumber = f.seq
	f.seq++
	n := len(p.Payload) + 12
	if f.pace != nil {
		f.pace.wait(n)
	}
	f.bytes.Add(uint64(n))
	if p.Marker {
		f.frames.Add(1)
	}
	f.track.WriteRTP(p)
}

// pacer espalha rajadas (quadros-chave) para não estourar buffers de roteador.
type pacer struct {
	mu     sync.Mutex
	rate   float64 // bytes/s
	tokens float64
	last   time.Time
}

func (p *pacer) setKbps(kbps int) {
	p.mu.Lock()
	p.rate = float64(kbps) * 1000 / 8 * 3
	p.mu.Unlock()
}

func (p *pacer) wait(n int) {
	p.mu.Lock()
	now := time.Now()
	burst := math.Max(64*1024, p.rate*0.004)
	p.tokens = math.Min(burst, p.tokens+now.Sub(p.last).Seconds()*p.rate)
	p.last = now
	p.tokens -= float64(n)
	var d time.Duration
	if p.tokens < 0 {
		d = time.Duration(-p.tokens / p.rate * 1e9)
	}
	p.mu.Unlock()
	if d > 300*time.Microsecond {
		time.Sleep(d)
	}
}

func listenRTP() (*net.UDPConn, *net.UDPConn, error) {
	lo := net.IPv4(127, 0, 0, 1)
	for i := 0; i < 50; i++ {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: lo})
		if err != nil {
			return nil, nil, err
		}
		port := c.LocalAddr().(*net.UDPAddr).Port
		c2, err := net.ListenUDP("udp4", &net.UDPAddr{IP: lo, Port: port + 1}) // RTCP do ffmpeg
		if err != nil {
			c.Close()
			continue
		}
		c.SetReadBuffer(32 << 20)
		go func() {
			b := make([]byte, 1500)
			for {
				if _, err := c2.Read(b); err != nil {
					return
				}
			}
		}()
		return c, c2, nil
	}
	return nil, nil, errors.New("sem porta UDP local livre")
}

/* ---------- Transmissor ---------- */

type viewer struct {
	pc      *webrtc.PeerConnection
	pending []webrtc.ICECandidateInit
	hasDesc bool
}

type Stats struct {
	State    string  `json:"state"` // idle | starting | live
	Viewers  int     `json:"viewers"`
	Mbps     float64 `json:"mbps"`
	FPS      float64 `json:"fps"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Pipeline string  `json:"pipeline"`
	Audio    string   `json:"audio"`
	Err      string   `json:"err"`
	Watchers []string `json:"watchers"` // códigos de quem está assistindo
}

// Signaler é o que o transmissor precisa para negociar com quem assiste.
// Na prática é o *node (servidor de sinalização); no benchmark, um laço local.
type Signaler interface {
	Me() string
	Send(dst, t string, d any) error
	SendOffer(dst, sdp string) error
	SendCandidate(dst, streamOwner string, c webrtc.ICECandidateInit) error
}

type Host struct {
	mu      sync.Mutex
	cfg     Config
	mon     Monitor
	ctx     context.Context
	cancel  context.CancelFunc
	vCancel context.CancelFunc
	api     *webrtc.API
	video   *forwarder
	audio   *forwarder
	viewers map[string]*viewer
	net     Signaler
	aeng    *audioEngine
	goodPipe string
	stats   Stats
}

func newHost() *Host { return &Host{stats: Stats{State: "idle"}} }

func (h *Host) Stats() Stats {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.stats
	s.Viewers = len(h.viewers)
	s.Watchers = []string{}
	for id := range h.viewers {
		s.Watchers = append(s.Watchers, id)
	}
	if h.aeng != nil {
		s.Audio = h.aeng.Mode()
	} else if s.State != "idle" {
		s.Audio = "desligado"
	}
	return s
}

func (h *Host) setStat(f func(*Stats)) {
	h.mu.Lock()
	f(&h.stats)
	h.mu.Unlock()
}

func (h *Host) Running() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cancel != nil
}

func (h *Host) Start(cfg Config, n Signaler) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancel != nil {
		return errors.New("já está transmitindo")
	}
	h.net = n
	cfg.normalize()
	mons := listMonitors()
	if len(mons) == 0 {
		return errors.New("nenhum monitor encontrado")
	}
	if cfg.Monitor < 0 || cfg.Monitor >= len(mons) {
		cfg.Monitor = 0
	}
	h.cfg, h.mon = cfg, mons[cfg.Monitor]

	m := &webrtc.MediaEngine{}
	vfb := []webrtc.RTCPFeedback{{Type: "nack"}, {Type: "nack", Parameter: "pli"}}
	vcap := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, RTCPFeedback: vfb,
		SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"}
	if cfg.Codec == "av1" {
		vcap = webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeAV1, ClockRate: 90000, RTCPFeedback: vfb}
	}
	acap := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
		SDPFmtpLine: "minptime=10;useinbandfec=1;stereo=1;sprop-stereo=1"}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: vcap, PayloadType: 96}, webrtc.RTPCodecTypeVideo); err != nil {
		return err
	}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: acap, PayloadType: 111}, webrtc.RTPCodecTypeAudio); err != nil {
		return err
	}
	ir := &interceptor.Registry{}
	resp, _ := nack.NewResponderInterceptor(nack.ResponderSize(8192)) // guarda ~2 s para retransmissões
	ir.Add(resp)
	webrtc.ConfigureRTCPReports(ir)
	h.api = webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(ir))

	vt, err := webrtc.NewTrackLocalStaticRTP(vcap, "video", "tela")
	if err != nil {
		return err
	}
	at, err := webrtc.NewTrackLocalStaticRTP(acap, "audio", "tela")
	if err != nil {
		return err
	}
	t0 := time.Now()
	h.video = &forwarder{track: vt, clock: 90000, t0: t0, pace: &pacer{last: t0}}
	h.audio = &forwarder{track: at, clock: 48000, t0: t0}
	h.viewers = map[string]*viewer{}
	h.ctx, h.cancel = context.WithCancel(context.Background())
	h.stats = Stats{State: "starting"}

	go h.videoLoop(h.ctx)
	go h.statsLoop(h.ctx)
	if cfg.Audio {
		h.startAudio()
	}
	return nil
}

// Update aplica mudanças ao vivo (resolução, fps, bitrate, encoder, áudio).
func (h *Host) Update(cfg Config) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancel == nil {
		return
	}
	cfg.normalize()
	old := h.cfg
	cfg.Codec = old.Codec // exige reiniciar a transmissão
	h.cfg = cfg
	if mons := listMonitors(); cfg.Monitor < len(mons) {
		h.mon = mons[cfg.Monitor]
	}
	if cfg.Height != old.Height || cfg.FPS != old.FPS || cfg.Mbps != old.Mbps || cfg.Encoder != old.Encoder || cfg.Monitor != old.Monitor {
		if cfg.Encoder != old.Encoder || cfg.Monitor != old.Monitor {
			h.goodPipe = ""
		}
		if h.vCancel != nil {
			h.vCancel()
		}
	}
	switch {
	case cfg.Audio && h.aeng == nil:
		h.startAudio()
	case !cfg.Audio && h.aeng != nil:
		h.aeng.Stop()
		h.aeng = nil
	case h.aeng != nil:
		h.aeng.SetExclusions(cfg.Exclude)
	}
}

func (h *Host) Stop() {
	h.mu.Lock()
	if h.cancel == nil {
		h.mu.Unlock()
		return
	}
	h.cancel()
	h.cancel = nil
	if h.aeng != nil {
		h.aeng.Stop()
		h.aeng = nil
	}
	n, viewers := h.net, h.viewers
	h.viewers = map[string]*viewer{}
	h.stats = Stats{State: "idle"}
	h.mu.Unlock()
	for id, v := range viewers {
		n.Send(id, "bye", nil)
		v.pc.Close()
	}
}

/* ---------- Espectadores (chamados pela sala, que já checou permissão) ---------- */

func (h *Host) viewerFor(id string) *viewer {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.viewers[id]
}

func (h *Host) HandleUnwatch(id string) {
	if v := h.viewerFor(id); v != nil {
		h.dropViewer(id, v)
	}
}

// KeepOnly desconecta quem não está mais na sala.
func (h *Host) KeepOnly(allowed map[string]bool) {
	h.mu.Lock()
	var gone []string
	for id := range h.viewers {
		if !allowed[id] {
			gone = append(gone, id)
		}
	}
	h.mu.Unlock()
	for _, id := range gone {
		h.HandleUnwatch(id)
	}
}

func (h *Host) HandleAnswer(id, sdp string) {
	v := h.viewerFor(id)
	if v == nil {
		return
	}
	if err := v.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp}); err != nil {
		log.Printf("answer %s: %v", id, err)
		h.dropViewer(id, v)
		return
	}
	h.mu.Lock()
	v.hasDesc = true
	pend := v.pending
	v.pending = nil
	h.mu.Unlock()
	for _, c := range pend {
		v.pc.AddICECandidate(c)
	}
}

func (h *Host) HandleCandidate(id string, c webrtc.ICECandidateInit) {
	h.mu.Lock()
	v := h.viewers[id]
	if v != nil && !v.hasDesc {
		v.pending = append(v.pending, c)
		v = nil
	}
	h.mu.Unlock()
	if v != nil {
		v.pc.AddICECandidate(c)
	}
}

func (h *Host) HandleWatch(id string) {
	h.mu.Lock()
	if h.cancel == nil {
		h.mu.Unlock()
		h.net.Send(id, "bye", nil)
		return
	}
	api, n, old := h.api, h.net, h.viewers[id]
	vtrack, atrack := h.video.track, h.audio.track
	me := n.Me()
	h.mu.Unlock()
	if old != nil {
		h.dropViewer(id, old)
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: iceServers})
	if err != nil {
		log.Printf("peerconnection: %v", err)
		return
	}
	v := &viewer{pc: pc}
	for _, t := range []webrtc.TrackLocal{vtrack, atrack} {
		tr, err := pc.AddTransceiverFromTrack(t, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
		if err != nil {
			pc.Close()
			return
		}
		go func() { // ler RTCP é necessário para NACK/relatórios funcionarem
			for {
				if _, _, err := tr.Sender().ReadRTCP(); err != nil {
					return
				}
			}
		}()
	}
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			n.SendCandidate(id, me, c.ToJSON())
		}
	})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed {
			h.dropViewer(id, v)
		}
	})
	h.mu.Lock()
	h.viewers[id] = v
	h.mu.Unlock()
	offer, err := pc.CreateOffer(nil)
	if err == nil {
		err = pc.SetLocalDescription(offer)
	}
	if err != nil {
		log.Printf("offer: %v", err)
		h.dropViewer(id, v)
		return
	}
	n.SendOffer(id, offer.SDP)
}

func (h *Host) dropViewer(id string, v *viewer) {
	h.mu.Lock()
	if h.viewers[id] == v {
		delete(h.viewers, id)
	}
	h.mu.Unlock()
	go v.pc.Close()
}


/* ---------- Vídeo ---------- */

func (h *Host) videoLoop(ctx context.Context) {
	for ctx.Err() == nil {
		h.mu.Lock()
		cfg, mon, good := h.cfg, h.mon, h.goodPipe
		h.mu.Unlock()
		cands := candidatePipelines(cfg, mon)
		for i, p := range cands { // o último pipeline que funcionou vai primeiro
			if p.key() == good {
				cands[0], cands[i] = cands[i], cands[0]
			}
		}
		if len(cands) == 0 {
			h.setStat(func(s *Stats) { s.Err = "nenhum encoder de vídeo disponível para " + strings.ToUpper(cfg.Codec) })
		}
		var lastErr string
		ran := false
		for _, p := range cands {
			vctx, vcancel := context.WithCancel(ctx)
			h.mu.Lock()
			h.vCancel = vcancel
			h.mu.Unlock()
			produced, errText := h.runVideo(vctx, p, cfg, mon)
			restarted := vctx.Err() != nil
			vcancel()
			if ctx.Err() != nil {
				return
			}
			if produced || restarted {
				ran = true
				break
			}
			lastErr = errText
			log.Printf("pipeline %s falhou: %s", p.key(), errText)
		}
		if !ran {
			if len(cands) > 0 {
				h.setStat(func(s *Stats) { s.Err = "não foi possível capturar/codificar: " + lastErr })
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}
}

// runVideo roda um FFmpeg até ele sair ou ctx ser cancelado.
func (h *Host) runVideo(ctx context.Context, p pipeline, cfg Config, mon Monitor) (produced bool, errText string) {
	conn, rtcpConn, err := listenRTP()
	if err != nil {
		return false, err.Error()
	}
	defer conn.Close()
	defer rtcpConn.Close()
	cmd, err := ffmpegCmd(videoArgs(p, cfg, mon, conn.LocalAddr().(*net.UDPAddr).Port)...)
	if err != nil {
		return false, err.Error()
	}
	tail := &tailWriter{}
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		return false, err.Error()
	}
	adoptChild(cmd.Process)
	exited := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			cmd.Process.Kill()
		case <-exited:
		}
	}()
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
		close(exited)
		errText = strings.TrimRight(tail.String(), " |")
	}()

	h.video.newSess = true
	h.video.pace.setKbps(cfg.kbps(mon))
	w, ht := mon.Width, mon.Height
	if p.scaleH > 0 {
		w, ht = (w*p.scaleH/ht)&^1, p.scaleH
	}
	buf := make([]byte, 2048)
	var pkt rtp.Packet
	n0 := h.video.frames.Load()
	for {
		conn.SetReadDeadline(time.Now().Add(8 * time.Second))
		n, err := conn.Read(buf)
		if err != nil {
			return produced, errText
		}
		if pkt.Unmarshal(buf[:n]) != nil {
			continue
		}
		h.video.write(&pkt)
		if !produced && h.video.frames.Load()-n0 > 10 {
			produced = true
			h.mu.Lock()
			h.goodPipe = p.key()
			h.stats.State, h.stats.Err = "live", ""
			h.stats.Pipeline = p.describe(cfg.Codec)
			h.stats.Width, h.stats.Height = w, ht
			h.mu.Unlock()
		}
	}
}

/* ---------- Áudio ---------- */

func (h *Host) startAudio() { // chamado com h.mu travado
	eng := newAudioEngine(h.cfg.Exclude)
	h.aeng = eng
	ctx := h.ctx
	go func() {
		for ctx.Err() == nil {
			select {
			case <-eng.stop:
				return
			default:
			}
			if err := h.runAudio(ctx, eng); err != nil {
				log.Printf("áudio: %v", err)
				time.Sleep(2 * time.Second)
			}
		}
	}()
}

func (h *Host) runAudio(ctx context.Context, eng *audioEngine) error {
	conn, rtcpConn, err := listenRTP()
	if err != nil {
		return err
	}
	defer conn.Close()
	defer rtcpConn.Close()
	cmd, err := ffmpegCmd(audioArgs(conn.LocalAddr().(*net.UDPAddr).Port)...)
	if err != nil {
		return err
	}
	stdin, _ := cmd.StdinPipe()
	if err := cmd.Start(); err != nil {
		return err
	}
	adoptChild(cmd.Process)
	defer cmd.Wait()
	defer cmd.Process.Kill()
	h.audio.newSess = true

	go func() {
		bw := bufio.NewWriterSize(stdin, 8192)
		eng.Run(func(s []float32) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			b := unsafe.Slice((*byte)(unsafe.Pointer(&s[0])), len(s)*4) // float32 little-endian
			if _, err := bw.Write(b); err != nil {
				return err
			}
			return bw.Flush()
		})
		stdin.Close()
		conn.Close()
	}()
	buf := make([]byte, 1500)
	var pkt rtp.Packet
	for {
		n, err := conn.Read(buf)
		if err != nil {
			select {
			case <-eng.stop:
				return nil
			default:
			}
			return err
		}
		if pkt.Unmarshal(buf[:n]) == nil {
			h.audio.write(&pkt)
		}
	}
}

/* ---------- Estatísticas ---------- */

func (h *Host) statsLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var lb, lf uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		b, f := h.video.bytes.Load(), h.video.frames.Load()
		h.setStat(func(s *Stats) {
			s.Mbps = float64(b-lb) * 8 / 1e6
			s.FPS = float64(f - lf)
		})
		lb, lf = b, f
	}
}

// tailWriter guarda as últimas linhas de stderr do FFmpeg (para mensagens de erro).
type tailWriter struct {
	mu    sync.Mutex
	lines []string
	part  string
}

func (t *tailWriter) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	parts := strings.Split(t.part+string(b), "\n")
	t.part = parts[len(parts)-1]
	for _, l := range parts[:len(parts)-1] {
		if l = strings.TrimSpace(l); l != "" {
			t.lines = append(t.lines, l)
		}
	}
	if len(t.lines) > 4 {
		t.lines = t.lines[len(t.lines)-4:]
	}
	return len(b), nil
}

func (t *tailWriter) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(append(t.lines, t.part), " | ")
}
