// Package audio captura o áudio do PC para transmitir, deixando de fora apps
// escolhidos (ex.: Discord) e o próprio familiashare.
//
// O Windows permite, por captura, incluir OU excluir UMA árvore de processos:
//   - nada a excluir além do app  → uma captura que exclui a árvore do próprio app
//   - há apps a excluir           → como o próprio app também precisa ficar de fora
//     (é ele que toca o som de quem você assiste; senão vira eco), são ≥ 2 árvores:
//     o motor captura cada app permitido que tem som, separadamente, e mistura.
//
// O Engine reavalia o plano a cada segundo (apps abrem e fecham) e entrega ao
// codificador blocos de 10 ms de PCM float32 estéreo a 48 kHz.
package audio

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/vitorbatalha/familiashare/internal/winapi"
)

const (
	SampleRate = 48000
	Channels   = 2
	chunk      = SampleRate / 100 // 10 ms
)

// target é uma captura: incluir ou excluir a árvore de processos de pid.
type target struct {
	pid     uint32
	exclude bool
}

// Engine decide o que capturar, mistura as fontes e entrega o áudio.
type Engine struct {
	mu      sync.Mutex
	excl    map[string]bool
	sources map[string]*loopbackSource
	mode    string
	stop    chan struct{}
	wake    chan struct{}
}

// New começa a capturar, excluindo os executáveis dados (ex.: "discord" ou "discord.exe").
func New(exclude []string) *Engine {
	a := &Engine{sources: map[string]*loopbackSource{}, stop: make(chan struct{}), wake: make(chan struct{}, 1)}
	a.SetExclusions(exclude)
	go a.planner()
	return a
}

// SetExclusions troca a lista de apps excluídos (vale na hora).
func (a *Engine) SetExclusions(names []string) {
	a.mu.Lock()
	a.excl = normalizeExclusions(names)
	a.mu.Unlock()
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func normalizeExclusions(names []string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" {
			continue
		}
		if !strings.HasSuffix(n, ".exe") {
			n += ".exe"
		}
		m[n] = true
	}
	return m
}

// Mode descreve o que está sendo capturado (para a interface).
func (a *Engine) Mode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

// Done fecha quando Stop é chamado.
func (a *Engine) Done() <-chan struct{} { return a.stop }

// Stop encerra todas as capturas.
func (a *Engine) Stop() {
	select {
	case <-a.stop:
	default:
		close(a.stop)
	}
}

// planTargets decide quais capturas fazer. É uma função pura (testável): recebe a
// foto dos processos e as sessões de áudio, e devolve as capturas e uma descrição.
func planTargets(excl map[string]bool, self uint32, procs map[uint32]procInfo, sessions []uint32) ([]target, string) {
	all := []target{{self, true}}
	if len(excl) == 0 {
		return all, "sistema inteiro"
	}
	isExcluded := func(pid uint32) bool {
		if pid == self || excl[procs[pid].name] {
			return true
		}
		for _, anc := range ancestors(procs, pid) {
			if anc == self || excl[procs[anc].name] {
				return true
			}
		}
		return false
	}
	// raízes das árvores excluídas que estão rodando
	var roots []uint32
	for pid, p := range procs {
		if !excl[p.name] {
			continue
		}
		if pp, ok := procs[p.parent]; ok && excl[pp.name] && p.parent != pid {
			continue // o pai também é excluído: a raiz é ele
		}
		roots = append(roots, pid)
	}
	if len(roots) == 0 {
		return all, "sistema inteiro (apps excluídos não estão abertos)"
	}
	// Não se pode incluir a árvore de um processo que tem um excluído (ou o próprio
	// app) como descendente — senão o excluído entraria junto.
	excludedDesc := map[uint32]bool{}
	for pid := range procs {
		if pid == self || excl[procs[pid].name] {
			for _, anc := range ancestors(procs, pid) {
				excludedDesc[anc] = true
			}
		}
	}
	cand := map[uint32]bool{}
	for _, pid := range sessions {
		if !isExcluded(pid) && !excludedDesc[pid] {
			cand[pid] = true
		}
	}
	var ts []target
	for pid := range cand {
		covered := false // já está dentro da árvore de outro candidato
		for _, anc := range ancestors(procs, pid) {
			if cand[anc] {
				covered = true
				break
			}
		}
		if !covered {
			ts = append(ts, target{pid, false})
		}
	}
	return ts, fmt.Sprintf("%d apps (exceto %d excluídos)", len(ts), len(roots))
}

// Run roda até Stop; out recebe blocos de 10 ms (float32 estéreo intercalado).
// Um erro de out encerra Run.
func (a *Engine) Run(out func([]float32) error) {
	buf := make([]float32, chunk*Channels)
	next := time.Now()
	for {
		select {
		case <-a.stop:
			return
		default:
		}
		next = next.Add(10 * time.Millisecond)
		if d := time.Until(next); d > 0 {
			time.Sleep(d)
		} else if d < -200*time.Millisecond {
			next = time.Now() // atrasou demais (PC travou?): recomeça o relógio
		}
		clear(buf)
		a.mu.Lock()
		for _, s := range a.sources {
			s.ring.mixInto(buf)
		}
		a.mu.Unlock()
		for i, v := range buf {
			buf[i] = float32(math.Max(-1, math.Min(1, float64(v))))
		}
		if err := out(buf); err != nil {
			return
		}
	}
}

// planner reavalia o plano a cada segundo (ou na hora, quando as exclusões mudam)
// e liga/desliga capturas para bater com ele.
func (a *Engine) planner() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	winapi.COMInit()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	self := uint32(os.Getpid())
	for {
		sessions, _ := sessionPIDs()
		a.mu.Lock()
		excl := a.excl
		a.mu.Unlock()
		targets, mode := planTargets(excl, self, processes(), sessions)
		want := map[string]target{}
		for _, t := range targets {
			want[t.key()] = t
		}
		a.mu.Lock()
		a.mode = mode
		for k, s := range a.sources {
			if _, ok := want[k]; !ok || s.dead.Load() {
				close(s.stop)
				delete(a.sources, k)
			}
		}
		for k, t := range want {
			if _, ok := a.sources[k]; !ok {
				a.sources[k] = startLoopback(t)
			}
		}
		a.mu.Unlock()
		select {
		case <-a.stop:
			a.mu.Lock()
			for k, s := range a.sources {
				close(s.stop)
				delete(a.sources, k)
			}
			a.mu.Unlock()
			return
		case <-a.wake:
		case <-t.C:
		}
	}
}
