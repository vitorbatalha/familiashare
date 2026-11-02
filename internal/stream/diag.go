package stream

// Registro da transmissão no log do app, para diagnosticar quem tem problemas para
// transmitir. Regras: linhas curtas, poucas por minuto, e nada pessoal — quem assiste
// aparece como "espectador 1", "espectador 2"… (nunca nome nem código de amigo), e
// endereços de rede não são registrados (só o tipo de conexão).

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/vitorbatalha/familiashare/internal/capture"
	"github.com/vitorbatalha/familiashare/internal/config"
)

func logf(format string, args ...any) { log.Printf("transmissão: "+format, args...) }

// settingsLine resume as configurações (o que foi pedido e o bitrate efetivo).
func settingsLine(cfg config.Config, mon capture.Monitor, monIdx, monCount int) string {
	res := "nativa"
	if cfg.Height > 0 {
		res = fmt.Sprintf("%dp", cfg.Height)
	}
	enc := cfg.Encoder
	if enc == "" {
		enc = "auto"
	}
	audio := "sem áudio"
	if cfg.Audio {
		audio = fmt.Sprintf("com áudio (%d apps excluídos)", len(cfg.Exclude))
	}
	return fmt.Sprintf("monitor %d/%d %dx%d · %s · resolução %s · %d fps · %d kbps · %s · encoder %s · %s",
		monIdx+1, monCount, mon.Width, mon.Height, mon.GPU, res, cfg.FPS, Kbps(cfg, mon), cfg.Codec, enc, audio)
}

func pipelineKeys(ps []pipeline) string {
	keys := make([]string, len(ps))
	for i, p := range ps {
		keys[i] = p.key()
	}
	return strings.Join(keys, ", ")
}

// ffmpegTail deixa o fim do stderr do FFmpeg em uma linha curta.
func ffmpegTail(s string) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " | ")), " ")
	if len(s) > 400 {
		s = "…" + s[len(s)-400:]
	}
	if s == "" {
		return "(sem mensagem)"
	}
	return s
}

// route descreve como a conexão com o espectador foi feita (ex.: "host/udp → srflx"),
// sem endereços: direto na rede local (host), pela internet (srflx) ou relay.
func route(pc *webrtc.PeerConnection) string {
	for _, tr := range pc.GetTransceivers() {
		s := tr.Sender()
		if s == nil || s.Transport() == nil {
			continue
		}
		p, err := s.Transport().ICETransport().GetSelectedCandidatePair()
		if err != nil || p == nil || p.Local == nil || p.Remote == nil {
			continue
		}
		return fmt.Sprintf("%s/%s → %s", p.Local.Typ, p.Local.Protocol, p.Remote.Typ)
	}
	return "?"
}

// minuteStats acumula o último minuto de transmissão para um resumo no log.
type minuteStats struct {
	n               int
	fpsSum, mbpsSum float64
	fpsMin          float64
}

func (m *minuteStats) add(fps, mbps float64) {
	if m.n == 0 || fps < m.fpsMin {
		m.fpsMin = fps
	}
	m.n++
	m.fpsSum += fps
	m.mbpsSum += mbps
}

// flush registra o resumo a cada 60 amostras (1 por segundo).
func (m *minuteStats) flush(viewers int) {
	if m.n < 60 {
		return
	}
	logf("último minuto: %.0f fps (mínimo %.0f) · %.1f Mbps · %d assistindo",
		m.fpsSum/float64(m.n), m.fpsMin, m.mbpsSum/float64(m.n), viewers)
	*m = minuteStats{}
}

func fmtDur(d time.Duration) string { return d.Round(time.Second).String() }
