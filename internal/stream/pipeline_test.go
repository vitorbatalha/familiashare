package stream

import (
	"slices"
	"strings"
	"testing"

	"github.com/vitorbatalha/familiashare/internal/capture"
	"github.com/vitorbatalha/familiashare/internal/config"
)

var nvidiaMon = capture.Monitor{Adapter: 0, Output: 0, Vendor: capture.VendorNVIDIA, Width: 2560, Height: 1600}

func cfg(mod func(*config.Config)) config.Config {
	c := config.Config{FPS: 60, Mbps: 12, Codec: "h264", Encoder: "auto"}
	if mod != nil {
		mod(&c)
	}
	return c
}

func TestKbps(t *testing.T) {
	if k := Kbps(cfg(nil), nvidiaMon); k != 12000 {
		t.Errorf("12 Mbps escolhidos → %d kbps", k)
	}
	// automático: 1080p60 ≈ 25 Mbps
	m1080 := capture.Monitor{Width: 1920, Height: 1080}
	if k := Kbps(cfg(func(c *config.Config) { c.Mbps = 0 }), m1080); k != 25000 {
		t.Errorf("automático 1080p60 = %d, quero 25000", k)
	}
	// AV1 precisa de ~25% menos
	if k := Kbps(cfg(func(c *config.Config) { c.Mbps = 0; c.Codec = "av1" }), m1080); k != 19000 {
		t.Errorf("automático AV1 1080p60 = %d, quero 19000", k)
	}
}

func TestZeroCopyComesFirstOnMatchingGPU(t *testing.T) {
	avail := map[string]bool{"h264_nvenc": true, "h264_amf": true, "h264_mf": true}
	ps := candidatePipelines(cfg(nil), nvidiaMon, avail)
	if len(ps) == 0 || ps[0] != (pipeline{enc: "nvenc", zeroCopy: true}) {
		t.Fatalf("primeira opção deveria ser NVENC sem cópia, veio %+v", ps)
	}
	last := ps[len(ps)-1]
	if last.zeroCopy {
		t.Errorf("as últimas opções devem ser via CPU (fallback): %+v", ps)
	}
}

func TestDownscaleNeverZeroCopy(t *testing.T) {
	// redimensionar na GPU não funciona com o FFmpeg 8.1: abaixo da nativa vai pela CPU
	avail := map[string]bool{"h264_nvenc": true}
	ps := candidatePipelines(cfg(func(c *config.Config) { c.Height = 1080 }), nvidiaMon, avail)
	for _, p := range ps {
		if p.zeroCopy || p.scaleH != 1080 {
			t.Errorf("pipeline inesperado: %+v", p)
		}
	}
}

func TestForcedEncoder(t *testing.T) {
	avail := map[string]bool{"h264_nvenc": true, "h264_amf": true}
	ps := candidatePipelines(cfg(func(c *config.Config) { c.Encoder = "amf" }), nvidiaMon, avail)
	for _, p := range ps {
		if p.enc != "amf" {
			t.Errorf("encoder forçado como amf, veio %+v", p)
		}
	}
}

func TestEveryEncoderForcesPeriodicKeyframes(t *testing.T) {
	// Regressão: o AMD AMF em low-latency ignorava -g e só gerava um quadro-chave no
	// início — quem entrava depois ficava em "aguardando o primeiro quadro".
	for _, enc := range []string{"nvenc", "amf", "qsv", "mf"} {
		args := videoArgs(pipeline{enc: enc}, cfg(nil), nvidiaMon, 5000)
		if !slices.Contains(args, "-force_key_frames") {
			t.Errorf("%s: falta -force_key_frames", enc)
		}
		joined := strings.Join(args, " ")
		if enc == "amf" || enc == "qsv" {
			if !strings.Contains(joined, "-forced_idr 1") {
				t.Errorf("%s: falta -forced_idr 1", enc)
			}
		}
		if enc == "nvenc" && !strings.Contains(joined, "-forced-idr 1") {
			t.Errorf("nvenc: falta -forced-idr 1")
		}
	}
}

func TestMediaFoundationWithoutHardwareFlag(t *testing.T) {
	// -hw_encoding 1 faz o h264_mf falhar (MF_E_UNSUPPORTED_D3D_TYPE)
	args := strings.Join(videoArgs(pipeline{enc: "mf"}, cfg(nil), nvidiaMon, 5000), " ")
	if strings.Contains(args, "hw_encoding") {
		t.Errorf("mf não pode usar -hw_encoding: %s", args)
	}
}

func TestZeroCopySkipsCPUFilters(t *testing.T) {
	zc := strings.Join(videoArgs(pipeline{enc: "nvenc", zeroCopy: true}, cfg(nil), nvidiaMon, 5000), " ")
	if strings.Contains(zc, "hwdownload") {
		t.Errorf("sem cópia não deveria baixar quadros para a CPU: %s", zc)
	}
	cpu := strings.Join(videoArgs(pipeline{enc: "nvenc", scaleH: 1080}, cfg(nil), nvidiaMon, 5000), " ")
	if !strings.Contains(cpu, "hwdownload") || !strings.Contains(cpu, "scale=-2:1080") {
		t.Errorf("caminho via CPU deveria baixar e redimensionar: %s", cpu)
	}
}
