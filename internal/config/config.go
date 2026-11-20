// Package config guarda as preferências e a identidade da pessoa
// (%LOCALAPPDATA%\familiashare\config.json).
package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type Friend struct {
	Code string `json:"code"`
	Name string `json:"name"`
	// Key é a chave pública do amigo, guardada na primeira mensagem assinada que chegou
	// dele. Depois disso, mensagens com o código dele só valem se vierem assinadas por ela.
	Key string `json:"key,omitempty"`
}

type Config struct {
	V          int      `json:"v"`
	Name       string   `json:"name"`
	MyCode     string   `json:"myCode"` // código de amigo (identidade permanente)
	Key        string   `json:"key"`    // chave privada Ed25519 (semente, base64): prova que o código é meu; nunca vai para a página nem para o log
	Friends    []Friend `json:"friends"`
	Pending    []string `json:"pending"` // pedidos de amizade enviados, ainda não aceitos
	RoomPublic bool     `json:"roomPublic"`

	// app
	OnClose       string `json:"onClose"`       // botão de fechar: tray | minimize | quit
	TrayHintSeen  bool   `json:"trayHintSeen"`  // já avisou que o app continua perto do relógio
	ShareHintSeen bool   `json:"shareHintSeen"` // já deu a dica de silenciar apps (eco) ao transmitir
	Theme         string `json:"theme"`         // cores do app (ver Themes)

	// volume escolhido para a tela de cada pessoa, em % (código → 0..99; 100 não é guardado)
	Volumes map[string]int `json:"volumes,omitempty"`

	// transmissão
	Monitor int      `json:"monitor"` // índice em capture.ListMonitors()
	Height  int      `json:"height"`  // 0 = nativa
	FPS     int      `json:"fps"`
	Mbps    int      `json:"mbps"`  // 0 = automático
	Codec   string   `json:"codec"` // h264 | av1
	Encoder string   `json:"encoder"`
	Audio   bool     `json:"audio"`
	Exclude []string `json:"exclude"`
	Camera  string   `json:"camera"` // câmera escolhida ("" = a padrão do Windows)
}

// Themes são os temas de cores do app; o primeiro é o padrão. As cores ficam na página
// (ui/index.html).
var Themes = []string{"escuro", "claro", "meia-noite", "roxo", "verde", "cafe"}

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
	if c.OnClose != "minimize" && c.OnClose != "quit" {
		c.OnClose = "tray"
	}
	if !slices.Contains(Themes, c.Theme) {
		c.Theme = Themes[0]
	}
	if c.MyCode == "" {
		c.MyCode = NewCode()
	}
	if c.PrivateKey() == nil {
		c.Key = NewKey()
	}
	for i, f := range c.Friends {
		if _, ok := ParsePublicKey(f.Key); !ok {
			c.Friends[i].Key = ""
		}
	}
	for code, v := range c.Volumes { // 100% é o padrão: não precisa ocupar espaço no arquivo
		if code != NormCode(code) || code == "" || v < 0 || v >= 100 {
			delete(c.Volumes, code)
		}
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

// NewKey gera uma chave privada nova (semente Ed25519 em base64).
func NewKey() string {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	return base64.StdEncoding.EncodeToString(priv.Seed())
}

// PrivateKey é a chave que assina as mensagens desta pessoa (nil se Key for inválida).
func (c Config) PrivateKey() ed25519.PrivateKey {
	seed, err := base64.StdEncoding.DecodeString(c.Key)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil
	}
	return ed25519.NewKeyFromSeed(seed)
}

// ParsePublicKey lê uma chave pública em base64.
func ParsePublicKey(s string) (ed25519.PublicKey, bool) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, false
	}
	return ed25519.PublicKey(b), true
}

// MaxVolumes limita quantas pessoas têm volume guardado (o arquivo não cresce sem fim
// com gente que entrou numa sala uma vez só).
const MaxVolumes = 200

// SetVolume guarda o volume (0..100%) escolhido para a tela de code. Devolve se mudou.
func (c *Config) SetVolume(code string, pct int) bool {
	code = NormCode(code)
	if code == "" {
		return false
	}
	pct = max(0, min(100, pct))
	old, had := c.Volumes[code]
	if pct == 100 {
		if !had {
			return false
		}
		delete(c.Volumes, code)
		return true
	}
	if had && old == pct {
		return false
	}
	if !had && len(c.Volumes) >= MaxVolumes {
		return false
	}
	if c.Volumes == nil {
		c.Volumes = map[string]int{}
	}
	c.Volumes[code] = pct
	return true
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
