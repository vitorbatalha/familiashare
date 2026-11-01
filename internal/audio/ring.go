package audio

import "sync"

// ring é o buffer de uma fonte de áudio: absorve o jitter da captura e a pequena
// diferença de clock entre a placa de som e o relógio do mixer.
type ring struct {
	mu     sync.Mutex
	buf    []float32
	primed bool
}

const (
	primeSamples = chunk * Channels * 2  // 20 ms acumulados antes de começar a tocar
	maxSamples   = chunk * Channels * 10 // >100 ms acumulados → descarta o excesso
	keepSamples  = chunk * Channels * 4  // ...ficando com os 40 ms mais recentes
)

func (r *ring) push(s []float32) {
	r.mu.Lock()
	r.buf = append(r.buf, s...)
	r.trim()
	r.mu.Unlock()
}

func (r *ring) pushSilence(n int) {
	r.mu.Lock()
	for i := 0; i < n; i++ {
		r.buf = append(r.buf, 0)
	}
	r.trim()
	r.mu.Unlock()
}

func (r *ring) trim() {
	if len(r.buf) > maxSamples {
		r.buf = append(r.buf[:0], r.buf[len(r.buf)-keepSamples:]...)
	}
}

// mixInto soma as próximas len(out) amostras em out.
func (r *ring) mixInto(out []float32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.primed {
		if len(r.buf) < primeSamples {
			return
		}
		r.primed = true
	}
	n := min(len(out), len(r.buf))
	for i := 0; i < n; i++ {
		out[i] += r.buf[i]
	}
	r.buf = append(r.buf[:0], r.buf[n:]...)
	if n < len(out) {
		r.primed = false // underflow: espera encher de novo
	}
}
