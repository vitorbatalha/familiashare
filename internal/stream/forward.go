package stream

// Do FFmpeg para o WebRTC: o FFmpeg manda RTP para uma porta UDP local; o forwarder
// reescreve números de sequência e timestamps (para que reinícios do FFmpeg pareçam
// uma transmissão contínua) e escreve na trilha WebRTC compartilhada por todos que
// assistem. O pacer espalha as rajadas dos quadros-chave.

import (
	"errors"
	"math"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

type forwarder struct {
	track   *webrtc.TrackLocalStaticRTP
	clock   float64 // Hz do relógio RTP (90000 vídeo, 48000 áudio)
	t0      time.Time
	seq     uint16
	newSess bool // o FFmpeg (re)começou: recalcular a base de timestamp
	srcBase uint32
	base    uint32
	pace    *pacer // nil = sem pacing (áudio)
	bytes   atomic.Uint64
	frames  atomic.Uint64 // pacotes com marker (= quadros de vídeo completos)
}

// write repassa um pacote. Cada reinício do FFmpeg gera SSRC/seq/timestamps novos;
// aqui tudo vira uma sequência contínua.
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

// pacer é um token bucket a 3× o bitrate: espalha rajadas (quadros-chave) para não
// estourar buffers de roteador, sem atrasar o fluxo normal.
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

// listenRTP abre um par de portas UDP locais (RTP + RTCP) para o FFmpeg enviar.
// O RTCP do FFmpeg é só descartado (se ninguém escutasse, o Windows faria o envio falhar).
func listenRTP() (*net.UDPConn, *net.UDPConn, error) {
	lo := net.IPv4(127, 0, 0, 1)
	for i := 0; i < 50; i++ {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: lo})
		if err != nil {
			return nil, nil, err
		}
		port := c.LocalAddr().(*net.UDPAddr).Port
		c2, err := net.ListenUDP("udp4", &net.UDPAddr{IP: lo, Port: port + 1})
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

// tailWriter guarda as últimas linhas do stderr do FFmpeg (para mensagens de erro).
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
	return strings.TrimRight(strings.Join(append(t.lines, t.part), " | "), " |")
}
