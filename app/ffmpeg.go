package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// FFmpeg vai comprimido dentro do .exe e é extraído na primeira transmissão.
//
//go:embed ffmpeg.exe.zst
var ffmpegZst []byte

var (
	ffmpegOnce sync.Once
	ffmpegFile string
	ffmpegErr  error
)

func ffmpegPath() (string, error) {
	ffmpegOnce.Do(func() {
		name := fmt.Sprintf("ffmpeg-%08x.exe", crc32.ChecksumIEEE(ffmpegZst))
		ffmpegFile = filepath.Join(dataDir, name)
		if st, err := os.Stat(ffmpegFile); err == nil && st.Size() > 0 {
			return
		}
		dec, err := zstd.NewReader(bytes.NewReader(ffmpegZst))
		if err != nil {
			ffmpegErr = err
			return
		}
		defer dec.Close()
		tmp := ffmpegFile + ".tmp"
		f, err := os.Create(tmp)
		if err != nil {
			ffmpegErr = err
			return
		}
		_, err = io.Copy(f, dec)
		f.Close()
		if err == nil {
			err = os.Rename(tmp, ffmpegFile)
		}
		if err != nil {
			os.Remove(tmp)
			ffmpegErr = err
		}
		// remove versões antigas
		old, _ := filepath.Glob(filepath.Join(dataDir, "ffmpeg-*.exe"))
		for _, o := range old {
			if o != ffmpegFile {
				os.Remove(o)
			}
		}
	})
	return ffmpegFile, ffmpegErr
}

func ffmpegCmd(args ...string) (*exec.Cmd, error) {
	p, err := ffmpegPath()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(p, append([]string{"-hide_banner", "-nostdin", "-loglevel", "error"}, args...)...)
	hideWindow(cmd)
	return cmd, nil
}

/* ---------- Encoders de hardware disponíveis ---------- */

var encoderFamilies = []string{"nvenc", "amf", "qsv", "mf"}

var (
	encOnce  sync.Once
	encAvail map[string]bool // "h264_nvenc" → true
)

func availableEncoders() map[string]bool {
	encOnce.Do(func() {
		encAvail = map[string]bool{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, fam := range encoderFamilies {
			for _, codec := range []string{"h264", "av1"} {
				name := codec + "_" + fam
				wg.Add(1)
				go func() {
					defer wg.Done()
					cmd, err := ffmpegCmd("-f", "lavfi", "-i", "color=black:s=1280x720:r=30", "-frames:v", "3", "-c:v", name, "-f", "null", "-")
					if err == nil && cmd.Run() == nil {
						mu.Lock()
						encAvail[name] = true
						mu.Unlock()
					}
				}()
			}
		}
		wg.Wait()
	})
	return encAvail
}

/* ---------- Pipeline de vídeo ---------- */

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

// candidatePipelines ordena as opções: zero-cópia primeiro (GPU do monitor = GPU do encoder),
// depois o caminho via CPU com o melhor encoder disponível.
func candidatePipelines(cfg Config, mon Monitor) []pipeline {
	avail := availableEncoders()
	fams := encoderFamilies
	if cfg.Encoder != "" && cfg.Encoder != "auto" {
		fams = []string{cfg.Encoder}
	}
	scaleH := 0
	if cfg.Height > 0 && cfg.Height < mon.Height {
		scaleH = cfg.Height
	}
	vendorOf := map[string]uint32{"nvenc": vendorNVIDIA, "amf": vendorAMD}
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

func videoArgs(p pipeline, cfg Config, mon Monitor, port int) []string {
	fps := cfg.FPS
	kbps := cfg.kbps(mon)
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
