package stream

// Como a tela vira vídeo: escolha do encoder e parâmetros do FFmpeg.
//
// O FFmpeg captura o monitor (filtro ddagrab, Desktop Duplication), codifica e manda
// RTP para uma porta local; o Host lê esse RTP e repassa via WebRTC. Os parâmetros
// abaixo foram ajustados à mão e verificados com o benchmark — mudar com cuidado.

import (
	"fmt"
	"math"
	"strconv"

	"github.com/vitorbatalha/familiashare/internal/capture"
	"github.com/vitorbatalha/familiashare/internal/config"
	"github.com/vitorbatalha/familiashare/internal/ffmpeg"
)

// Kbps é o bitrate de vídeo: o escolhido, ou o automático, proporcional a
// pixels × fps (1080p60 ≈ 25 Mbps; AV1 precisa de ~25% menos).
func Kbps(c config.Config, mon capture.Monitor) int {
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

type pipeline struct {
	enc      string // nvenc|amf|qsv|mf
	zeroCopy bool   // captura e codifica sem sair da GPU
	scaleH   int    // 0 = resolução nativa
}

func (p pipeline) key() string { return fmt.Sprintf("%s/%v/%d", p.enc, p.zeroCopy, p.scaleH) }

func (p pipeline) describe(codec string) string {
	s := map[string]string{"nvenc": "NVIDIA NVENC", "amf": "AMD AMF", "qsv": "Intel QuickSync", "mf": "Media Foundation"}[p.enc]
	s += " " + map[string]string{"h264": "H.264", "av1": "AV1"}[codec]
	if p.zeroCopy {
		s += " · captura na GPU"
	} else {
		s += " · via CPU"
	}
	return s
}

// candidatePipelines ordena as opções: zero-cópia primeiro (GPU do monitor = GPU do
// encoder), depois o caminho via CPU com o melhor encoder disponível. O Host tenta
// em ordem até uma funcionar.
func candidatePipelines(cfg config.Config, mon capture.Monitor, avail map[string]bool) []pipeline {
	fams := ffmpeg.Families
	if cfg.Encoder != "" && cfg.Encoder != "auto" {
		fams = []string{cfg.Encoder}
	}
	scaleH := 0
	if cfg.Height > 0 && cfg.Height < mon.Height {
		scaleH = cfg.Height
	}
	// redimensionar na GPU não funciona (scale_d3d11 falha com NVENC no FFmpeg 8.1),
	// então resolução abaixo da nativa sempre passa pela CPU
	vendorOf := map[string]uint32{"nvenc": capture.VendorNVIDIA, "amf": capture.VendorAMD}
	var out []pipeline
	if scaleH == 0 {
		for _, f := range fams {
			if avail[cfg.Codec+"_"+f] && vendorOf[f] != 0 && vendorOf[f] == mon.Vendor {
				out = append(out, pipeline{enc: f, zeroCopy: true})
			}
		}
		// NVENC também aceita texturas de outra GPU em notebooks híbridos; vale tentar
		for _, f := range fams {
			if avail[cfg.Codec+"_"+f] && vendorOf[f] != 0 && vendorOf[f] != mon.Vendor {
				out = append(out, pipeline{enc: f, zeroCopy: true})
			}
		}
	}
	for _, f := range fams {
		if avail[cfg.Codec+"_"+f] {
			out = append(out, pipeline{enc: f, scaleH: scaleH})
		}
	}
	return out
}

func videoArgs(p pipeline, cfg config.Config, mon capture.Monitor, port int) []string {
	fps := cfg.FPS
	kbps := Kbps(cfg, mon)
	a := []string{}
	if mon.Adapter != 0 {
		a = append(a, "-init_hw_device", "d3d11va=gpu:"+strconv.Itoa(mon.Adapter), "-filter_hw_device", "gpu")
	}
	a = append(a, "-f", "lavfi", "-i", fmt.Sprintf("ddagrab=output_idx=%d:framerate=%d:dup_frames=1", mon.Output, fps))
	if !p.zeroCopy {
		vf := "hwdownload,format=bgra"
		if p.scaleH > 0 {
			vf += fmt.Sprintf(",scale=-2:%d:flags=bilinear", p.scaleH)
		}
		if p.enc == "qsv" || p.enc == "mf" {
			vf += ",format=nv12"
		}
		a = append(a, "-filter_threads", "4", "-vf", vf)
	}
	b := strconv.Itoa(kbps) + "k"
	// VBV de ~3 quadros: quadros-chave pequenos = sem picos de latência
	buf := strconv.Itoa(max(kbps*3/fps, 500)) + "k"
	// Quadro-chave (IDR) a cada 2 s é o que permite a quem entra (ou perde pacotes)
	// começar a ver. Alguns encoders ignoram -g: o AMD AMF em low-latency só gera UM
	// IDR no início — quem entrava depois ficava em "aguardando o primeiro quadro".
	// Por isso o IDR é forçado explicitamente.
	a = append(a, "-c:v", cfg.Codec+"_"+p.enc, "-g", strconv.Itoa(fps*2), "-bf", "0",
		"-b:v", b, "-maxrate", b, "-bufsize", buf,
		"-force_key_frames", "expr:gte(t,n_forced*2)")
	switch p.enc {
	case "nvenc":
		a = append(a, "-preset", "p4", "-tune", "ll", "-rc", "cbr", "-zerolatency", "1", "-forced-idr", "1")
		if cfg.Codec == "h264" {
			a = append(a, "-profile:v", "high")
		}
	case "amf":
		a = append(a, "-usage", "lowlatency", "-quality", "balanced", "-rc", "cbr", "-forced_idr", "1")
	case "qsv":
		a = append(a, "-preset", "veryfast", "-async_depth", "1", "-forced_idr", "1")
	case "mf":
		// -hw_encoding 1 falha (MF_E_UNSUPPORTED_D3D_TYPE) com quadros vindos da CPU
		a = append(a, "-rate_control", "cbr")
	}
	if cfg.Codec == "h264" {
		a = append(a, "-bsf:v", "dump_extra=freq=keyframe") // SPS/PPS em todo quadro-chave
	}
	return append(a, "-strict", "experimental", "-f", "rtp", "-payload_type", "96",
		fmt.Sprintf("rtp://127.0.0.1:%d?pkt_size=1200", port))
}

func audioArgs(port int) []string {
	return []string{"-probesize", "32", "-analyzeduration", "0", "-fflags", "nobuffer",
		"-f", "f32le", "-ar", "48000", "-ac", "2", "-i", "pipe:0",
		"-c:a", "libopus", "-b:a", "256k", "-application", "audio", "-frame_duration", "10",
		"-f", "rtp", "-payload_type", "111", fmt.Sprintf("rtp://127.0.0.1:%d?pkt_size=1200", port)}
}
