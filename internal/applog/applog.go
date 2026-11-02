// Package applog é o log do app em disco (%LOCALAPPDATA%\familiashare\familiashare.log),
// pensado para diagnosticar problemas de quem usa:
//
//   - persiste entre aberturas do app, mas com tamanho limitado: ao passar de Max, o
//     arquivo vira familiashare.anterior.log (substituindo o anterior), então os dois
//     juntos nunca passam de 2×Max;
//   - tudo que é escrito passa por Scrub, que tira dados pessoais (pasta do usuário,
//     nome do usuário e do PC, endereços IP e o que for registrado com Redact).
//
// O código do app não deve registrar nomes nem códigos de amigos; Scrub é a segunda
// camada de proteção.
package applog

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// Max é o tamanho de cada arquivo de log.
const Max = 512 << 10

// Writer é um io.Writer para log.SetOutput.
type Writer struct {
	mu      sync.Mutex
	path    string
	f       *os.File
	size    int64
	max     int64
	secrets []string
}

// Open abre (ou cria) o log em path, acrescentando ao que já existe.
func Open(path string, max int64) (*Writer, error) {
	w := &Writer{path: path, max: max, secrets: defaultSecrets()}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Writer) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	st, _ := f.Stat()
	w.f, w.size = f, st.Size()
	return nil
}

// Previous é o caminho do arquivo anterior (rotacionado).
func Previous(path string) string {
	ext := filepath.Ext(path)
	return strings.TrimSuffix(path, ext) + ".anterior" + ext
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	b := []byte(Scrub(string(p), w.secrets))
	if w.size+int64(len(b)) > w.max && w.size > 0 {
		w.f.Close()
		os.Remove(Previous(w.path))
		os.Rename(w.path, Previous(w.path))
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(b)
	w.size += int64(n)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// Redact faz o texto s nunca aparecer no log (ex.: o próprio código de amigo).
func (w *Writer) Redact(s ...string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, x := range s {
		if len(x) >= 3 {
			w.secrets = append(w.secrets, x)
		}
	}
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}

// TrimFile limita arquivos que outros escrevem (ex.: crash.log, escrito pelo runtime
// do Go): se passou de max, vira o ".anterior" e recomeça vazio.
func TrimFile(path string, max int64) {
	if st, err := os.Stat(path); err == nil && st.Size() > max {
		os.Remove(Previous(path))
		os.Rename(path, Previous(path))
	}
}

func defaultSecrets() []string {
	var s []string
	if h, err := os.UserHomeDir(); err == nil {
		s = append(s, h)
	}
	for _, v := range []string{"USERNAME", "COMPUTERNAME"} {
		if x := os.Getenv(v); len(x) >= 3 {
			s = append(s, x)
		}
	}
	return s
}

var (
	ipv4 = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	// completo (8 grupos) ou abreviado com "::"; horas como 00:47:00 não casam
	ipv6 = regexp.MustCompile(`(?i)\b(?:[0-9a-f]{1,4}:){7}[0-9a-f]{1,4}\b|(?:[0-9a-f]{1,4}:)*[0-9a-f]{0,4}::[0-9a-f:]*[0-9a-f]`)
)

// Scrub tira dados pessoais de s: cada um dos secrets (sem diferenciar maiúsculas) e
// endereços IP.
func Scrub(s string, secrets []string) string {
	// os mais longos primeiro: "PC-DA-MARIA" antes de "Maria"
	secrets = slices.Clone(secrets)
	slices.SortFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	for _, x := range secrets {
		re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(x))
		s = re.ReplaceAllString(s, "‹oculto›")
	}
	s = ipv4.ReplaceAllString(s, "‹ip›")
	s = ipv6.ReplaceAllString(s, "‹ip›")
	return s
}
