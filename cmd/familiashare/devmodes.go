package main

import (
	"log"
	"math"
	"os"
	"strings"
	"time"

	"github.com/vitorbatalha/familiashare/internal/audio"
)

// audioTest (FAMILIASHARE_AUDIOTEST=1) imprime, a cada segundo, o volume (RMS) do
// áudio capturado e o modo de captura. Com FAMILIASHARE_EXCLUDE=discord,spotify dá
// para conferir se um app some do áudio: toque algo nele e veja o RMS ir a zero.
func audioTest() {
	log.SetOutput(os.Stdout)
	var excl []string
	if e := os.Getenv("FAMILIASHARE_EXCLUDE"); e != "" {
		excl = strings.Split(e, ",")
	}
	eng := audio.New(excl)
	var sum float64
	var n int
	last := time.Now()
	eng.Run(func(s []float32) error {
		for _, v := range s {
			sum += float64(v) * float64(v)
		}
		n += len(s)
		if time.Since(last) >= time.Second {
			log.Printf("rms=%.4f modo=%q", math.Sqrt(sum/float64(n)), eng.Mode())
			sum, n, last = 0, 0, time.Now()
		}
		return nil
	})
}
