package stream

// Loops de mídia: mantêm um FFmpeg de vídeo e um de áudio rodando enquanto há
// transmissão, reiniciando quando as configurações mudam ou algo falha.

import (
	"bufio"
	"context"
	"log"
	"net"
	"strings"
	"time"
	"unsafe"

	"github.com/pion/rtp"
	"github.com/vitorbatalha/familiashare/internal/audio"
	"github.com/vitorbatalha/familiashare/internal/capture"
	"github.com/vitorbatalha/familiashare/internal/config"
	"github.com/vitorbatalha/familiashare/internal/ffmpeg"
	"github.com/vitorbatalha/familiashare/internal/winapi"
)

/* ---------- Vídeo ---------- */

// videoLoop tenta os pipelines em ordem até um funcionar; quando ele para (mudança de
// configuração ou falha), recomeça — pelo último que funcionou.
func (h *Host) videoLoop(ctx context.Context) {
	for ctx.Err() == nil {
		h.mu.Lock()
		cfg, mon, good := h.cfg, h.mon, h.goodPipe
		h.mu.Unlock()
		cands := candidatePipelines(cfg, mon, ffmpeg.Available())
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

// runVideo roda um FFmpeg até ele sair ou ctx ser cancelado. produced = chegou a
// gerar vídeo (então o pipeline funciona nesta máquina).
func (h *Host) runVideo(ctx context.Context, p pipeline, cfg config.Config, mon capture.Monitor) (produced bool, errText string) {
	conn, rtcpConn, err := listenRTP()
	if err != nil {
		return false, err.Error()
	}
	defer conn.Close()
	defer rtcpConn.Close()
	cmd, err := ffmpeg.Command(videoArgs(p, cfg, mon, conn.LocalAddr().(*net.UDPAddr).Port)...)
	if err != nil {
		return false, err.Error()
	}
	tail := &tailWriter{}
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		return false, err.Error()
	}
	winapi.AdoptChild(cmd.Process)
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
		errText = tail.String()
	}()

	h.video.newSess = true
	h.video.pace.setKbps(Kbps(cfg, mon))
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
	eng := audio.New(h.cfg.Exclude)
	h.aeng = eng
	ctx := h.ctx
	go func() {
		for ctx.Err() == nil {
			select {
			case <-eng.Done():
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

// runAudio liga a captura (Engine) a um FFmpeg que codifica em Opus e manda RTP.
func (h *Host) runAudio(ctx context.Context, eng *audio.Engine) error {
	conn, rtcpConn, err := listenRTP()
	if err != nil {
		return err
	}
	defer conn.Close()
	defer rtcpConn.Close()
	cmd, err := ffmpeg.Command(audioArgs(conn.LocalAddr().(*net.UDPAddr).Port)...)
	if err != nil {
		return err
	}
	stdin, _ := cmd.StdinPipe()
	if err := cmd.Start(); err != nil {
		return err
	}
	winapi.AdoptChild(cmd.Process)
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
			case <-eng.Done():
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
