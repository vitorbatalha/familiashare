package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Friend struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type Config struct {
	V          int      `json:"v"`
	Name       string   `json:"name"`
	MyCode     string   `json:"myCode"`  // código de amigo (identidade permanente)
	Friends    []Friend `json:"friends"`
	Pending    []string `json:"pending"` // pedidos de amizade enviados, ainda não aceitos
	RoomPublic bool     `json:"roomPublic"`

	// transmissão
	Monitor int      `json:"monitor"` // índice em listMonitors()
	Height  int      `json:"height"`  // 0 = nativa
	FPS     int      `json:"fps"`
	Mbps    int      `json:"mbps"`  // 0 = automático
	Codec   string   `json:"codec"` // h264 | av1
	Encoder string   `json:"encoder"`
	Audio   bool     `json:"audio"`
	Exclude []string `json:"exclude"`
}

const configVersion = 2

func (c *Config) normalize() {
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
		c.MyCode = newCode()
	}
	if c.Name == "" {
		c.Name = os.Getenv("USERNAME")
	}
	if c.Name == "" {
		c.Name = "Eu"
	}
	if c.V < 2 && c.Mbps == 0 { // o automático antigo era alto demais para internet doméstica
		c.Mbps = 12
	}
	c.V = configVersion
}

func configPath() string { return filepath.Join(dataDir, "config.json") }

func loadConfig() Config {
	c := Config{V: configVersion, Audio: true, FPS: 60, Mbps: 12, Codec: "h264", Encoder: "auto", RoomPublic: true}
	if b, err := os.ReadFile(configPath()); err == nil {
		c.V = 0
		json.Unmarshal(b, &c)
	}
	c.normalize()
	return c
}

func saveConfig(c Config) {
	b, _ := json.MarshalIndent(c, "", "  ")
	os.WriteFile(configPath(), b, 0o644)
}
