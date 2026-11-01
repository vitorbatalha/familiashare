// Package stream é o lado de quem transmite: captura a tela e o áudio (via FFmpeg),
// e envia por WebRTC para cada pessoa que assiste.
//
// A tela é codificada UMA vez; todos que assistem recebem a mesma trilha (cada um
// com sua própria conexão WebRTC). Quem decide quem pode assistir é a sala
// (internal/social), que chama HandleWatch só para membros.
package stream

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/nack"
	"github.com/pion/webrtc/v4"
	"github.com/vitorbatalha/familiashare/internal/audio"
	"github.com/vitorbatalha/familiashare/internal/capture"
	"github.com/vitorbatalha/familiashare/internal/config"
)

var iceServers = []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302", "stun:stun.cloudflare.com:3478"}}}

// Signaler é o que o transmissor precisa para negociar com quem assiste.
// Na prática é o *signaling.Node; no benchmark, um laço local.
type Signaler interface {
	Me() string
	Send(dst, t string, d any) error
	SendOffer(dst, sdp string) error
	SendCandidate(dst, streamOwner string, c webrtc.ICECandidateInit) error
}

// Stats é o estado da transmissão, para a interface.
type Stats struct {
	State    string   `json:"state"` // idle | starting | live
	Viewers  int      `json:"viewers"`
	Mbps     float64  `json:"mbps"`
	FPS      float64  `json:"fps"`
	Width    int      `json:"width"`
	Height   int      `json:"height"`
	Pipeline string   `json:"pipeline"`
	Audio    string   `json:"audio"`
	Err      string   `json:"err"`
	Watchers []string `json:"watchers"` // códigos de quem está assistindo
}

type viewer struct {
	pc      *webrtc.PeerConnection
	pending []webrtc.ICECandidateInit // candidatos que chegaram antes da resposta
	hasDesc bool
}

// Host é a transmissão desta pessoa (no máximo uma por vez).
type Host struct {
	mu       sync.Mutex
	cfg      config.Config
	mon      capture.Monitor
	ctx      context.Context
	cancel   context.CancelFunc
	vCancel  context.CancelFunc // reinicia só o FFmpeg de vídeo
	api      *webrtc.API
	video    *forwarder
	audio    *forwarder
	viewers  map[string]*viewer
	net      Signaler
	aeng     *audio.Engine
	goodPipe string // último pipeline que funcionou
	stats    Stats
}

func New() *Host { return &Host{stats: Stats{State: "idle"}} }

// Stats devolve o estado atual.
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

// FramesSent conta os quadros de vídeo enviados desde o início (para o benchmark).
func (h *Host) FramesSent() uint64 {
	h.mu.Lock()
	v := h.video
	h.mu.Unlock()
	if v == nil {
		return 0
	}
	return v.frames.Load()
}

func (h *Host) setStat(f func(*Stats)) {
	h.mu.Lock()
	f(&h.stats)
	h.mu.Unlock()
}

// Running diz se há transmissão.
func (h *Host) Running() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cancel != nil
}

// Start começa a transmitir com as configurações dadas.
func (h *Host) Start(cfg config.Config, n Signaler) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancel != nil {
		return errors.New("já está transmitindo")
	}
	h.net = n
	cfg.Normalize()
	mons := capture.ListMonitors()
	if len(mons) == 0 {
		return errors.New("nenhum monitor encontrado")
	}
	if cfg.Monitor < 0 || cfg.Monitor >= len(mons) {
		cfg.Monitor = 0
	}
	h.cfg, h.mon = cfg, mons[cfg.Monitor]

	// Só o codec escolhido é oferecido; Opus estéreo para o áudio.
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

	vt, err := webrtc.NewTrackLocalStaticRTP(vcap, "video", "familiashare")
	if err != nil {
		return err
	}
	at, err := webrtc.NewTrackLocalStaticRTP(acap, "audio", "familiashare")
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
// O codec não muda ao vivo: exige reiniciar a transmissão.
func (h *Host) Update(cfg config.Config) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancel == nil {
		return
	}
	cfg.Normalize()
	old := h.cfg
	cfg.Codec = old.Codec
	h.cfg = cfg
	if mons := capture.ListMonitors(); cfg.Monitor < len(mons) {
		h.mon = mons[cfg.Monitor]
	}
	if cfg.Height != old.Height || cfg.FPS != old.FPS || cfg.Mbps != old.Mbps || cfg.Encoder != old.Encoder || cfg.Monitor != old.Monitor {
		if cfg.Encoder != old.Encoder || cfg.Monitor != old.Monitor {
			h.goodPipe = ""
		}
		if h.vCancel != nil {
			h.vCancel() // o videoLoop reinicia o FFmpeg com as novas configurações
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

// Stop encerra a transmissão e avisa quem estava assistindo.
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

// HandleUnwatch desconecta id.
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

// HandleAnswer recebe a resposta WebRTC de id.
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

// HandleCandidate recebe um candidato ICE de id.
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

// HandleWatch cria a conexão para id assistir e manda a oferta.
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
