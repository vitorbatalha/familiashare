// Package config guarda as preferências e a identidade da pessoa
// (%LOCALAPPDATA%\familiashare\config.json).
package config

import (
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type Friend struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type Config struct {
	V          int      `json:"v"`
	Name       string   `json:"name"`
	MyCode     string   `json:"myCode"` // código de amigo (identidade permanente)
	Friends    []Friend `json:"friends"`
	Pending    []string `json:"pending"` // pedidos de amizade enviados, ainda não aceitos
	RoomPublic bool     `json:"roomPublic"`

	// transmissão
	Monitor int      `json:"monitor"` // índice em capture.ListMonitors()
	Height  int      `json:"height"`  // 0 = nativa
	FPS     int      `json:"fps"`
	Mbps    int      `json:"mbps"`  // 0 = automático
	Codec   string   `json:"codec"` // h264 | av1
	Encoder string   `json:"encoder"`
	Audio   bool     `json:"audio"`
	Exclude []string `json:"exclude"`
}

// Version é a versão atual do formato. Ao subir, adicione a migração em Normalize.
const Version = 2

// Normalize preenche valores ausentes e migra configs antigas.
func (c *Config) Normalize() {
	if c.FPS != 30 && c.FPS != 60 {
		c.FPS = 60
	}
	if c.Codec != "av1" {
		c.Codec = "h264"
	}
	if c.Encoder == "" {
		c.Encoder = "auto"
	}
	if c.MyCode == "" {
		c.MyCode = NewCode()
	}
	if c.Name == "" {
		c.Name = os.Getenv("USERNAME")
	}
	if c.Name == "" {
		c.Name = "Eu"
	}
	if c.V < 2 && c.Mbps == 0 { // v1→v2: o automático antigo era alto demais para internet doméstica
		c.Mbps = 12
	}
	c.V = Version
}

func Path(dir string) string { return filepath.Join(dir, "config.json") }

// Load lê a config de dir (ou devolve os padrões, se não existir).
func Load(dir string) Config {
	c := Config{V: Version, Audio: true, FPS: 60, Mbps: 12, Codec: "h264", Encoder: "auto", RoomPublic: true}
	if b, err := os.ReadFile(Path(dir)); err == nil {
		c.V = 0 // arquivo sem "v" é da versão 1
		json.Unmarshal(b, &c)
	}
	c.Normalize()
	return c
}

// Save grava a config em dir.
func Save(dir string, c Config) error {
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(Path(dir), b, 0o644)
}

const codeAlphabet = "abcdefghjkmnpqrstuvwxyz23456789" // sem 0/o, 1/l/i: fáceis de confundir

// NewCode gera um código de amigo (8 caracteres, ~40 bits).
func NewCode() string {
	b := make([]byte, 8)
	rand.Read(b)
	for i := range b {
		b[i] = codeAlphabet[int(b[i])%len(codeAlphabet)]
	}
	return string(b)
}

// NormCode limpa um código digitado: minúsculas, só letras e números.
func NormCode(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
