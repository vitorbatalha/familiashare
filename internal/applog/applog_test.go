package applog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScrub(t *testing.T) {
	secrets := []string{`C:\Users\Maria`, "Maria", "PC-DA-MARIA", "k7m2pq9x"}
	cases := map[string]string{
		`erro em C:\Users\Maria\AppData\Local\familiashare\ffmpeg.exe`: `erro em ‹oculto›\AppData\Local\familiashare\ffmpeg.exe`,
		`c:\users\maria\x`:                        `‹oculto›\x`, // Windows não diferencia maiúsculas
		"usuário MARIA no pc-da-maria":            "usuário ‹oculto› no ‹oculto›",
		"meu código k7m2pq9x":                     "meu código ‹oculto›",
		"candidato 192.168.0.12 porta 5000":       "candidato ‹ip› porta 5000",
		"ipv6 2804:14c:5b82:8f00::1a2b ok":        "ipv6 ‹ip› ok",
		"ipv6 fe80::1c2d:3e4f:5a6b:7c8d%12":       "ipv6 ‹ip›%12",
		"ffmpeg n8.1.3, 2560x1440 @ 60 fps":       "ffmpeg n8.1.3, 2560x1440 @ 60 fps", // versões e horas ficam
		"2026/11/02 00:47:00 transmissão: início": "2026/11/02 00:47:00 transmissão: início",
	}
	for in, want := range cases {
		if got := Scrub(in, secrets); got != want {
			t.Errorf("Scrub(%q)\n  = %q\n  quero %q", in, got, want)
		}
	}
}

func TestRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.log")
	w, err := Open(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("a", 39) + "\n" // 40 bytes
	for i := 0; i < 7; i++ {
		w.Write([]byte(line))
	}
	w.Close()
	cur, _ := os.ReadFile(path)
	prev, _ := os.ReadFile(Previous(path))
	if len(cur) > 100 || len(prev) > 100 {
		t.Errorf("arquivos passaram do limite: atual %d, anterior %d", len(cur), len(prev))
	}
	if len(cur)+len(prev) < 100 { // sempre sobra pelo menos um arquivo cheio de linhas recentes
		t.Errorf("rotação perdeu linhas recentes demais: atual %d, anterior %d", len(cur), len(prev))
	}
	if Previous(path) != filepath.Join(filepath.Dir(path), "x.anterior.log") {
		t.Errorf("Previous = %s", Previous(path))
	}

	// reabrir continua acrescentando, sem apagar
	w, _ = Open(path, 100)
	w.Write([]byte("b\n"))
	w.Close()
	if b, _ := os.ReadFile(path); !strings.HasSuffix(string(b), "b\n") || len(b) <= 2 {
		t.Errorf("reabrir apagou o log: %q", b)
	}
}

func TestRedact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.log")
	w, _ := Open(path, 1<<20)
	w.Redact("abcd2345", "xy") // curto demais para esconder sem estragar o texto
	w.Write([]byte("código abcd2345, xy\n"))
	w.Close()
	if b, _ := os.ReadFile(path); string(b) != "código ‹oculto›, xy\n" {
		t.Errorf("log = %q", b)
	}
}

func TestTrimFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crash.log")
	os.WriteFile(path, make([]byte, 300), 0o644)
	TrimFile(path, 200)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("arquivo grande deveria ter sido movido")
	}
	if st, err := os.Stat(Previous(path)); err != nil || st.Size() != 300 {
		t.Error("o conteúdo deveria estar no .anterior")
	}
	os.WriteFile(path, make([]byte, 50), 0o644)
	TrimFile(path, 200)
	if _, err := os.Stat(path); err != nil {
		t.Error("arquivo pequeno não deveria mudar")
	}
}
