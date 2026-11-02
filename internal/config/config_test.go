package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaultsWhenMissing(t *testing.T) {
	c := Load(t.TempDir())
	if c.V != Version || c.FPS != 60 || c.Mbps != 12 || c.Codec != "h264" || c.Encoder != "auto" || !c.Audio {
		t.Errorf("padrões inesperados: %+v", c)
	}
	if len(c.MyCode) != 8 {
		t.Errorf("código de amigo deveria ser gerado com 8 caracteres: %q", c.MyCode)
	}
}

func TestMigrationV1SetsBitrateTo12(t *testing.T) {
	// v1 não tinha "v" e usava mbps 0 (automático, alto demais para internet doméstica)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"name":"Ana","myCode":"abcdefgh","mbps":0}`), 0o644)
	c := Load(dir)
	if c.Mbps != 12 || c.V != Version {
		t.Errorf("migração v1→v2: mbps=%d v=%d", c.Mbps, c.V)
	}
	if c.MyCode != "abcdefgh" || c.Name != "Ana" {
		t.Errorf("identidade não pode mudar na migração: %+v", c)
	}
}

func TestV2KeepsAutomaticBitrate(t *testing.T) {
	// em v2, mbps 0 é uma escolha explícita ("Automático") e deve ser mantida
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"v":2,"myCode":"abcdefgh","mbps":0}`), 0o644)
	if c := Load(dir); c.Mbps != 0 {
		t.Errorf("mbps = %d, quero 0 (automático)", c.Mbps)
	}
}

func TestOnCloseDefaultsToTray(t *testing.T) {
	// configs antigas (sem onClose) passam a ir para perto do relógio ao fechar
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"v":2,"myCode":"abcdefgh"}`), 0o644)
	if c := Load(dir); c.OnClose != "tray" {
		t.Errorf("onClose = %q, quero tray", c.OnClose)
	}
	for _, v := range []string{"tray", "minimize", "quit"} {
		c := Config{OnClose: v}
		c.Normalize()
		if c.OnClose != v {
			t.Errorf("onClose %q virou %q", v, c.OnClose)
		}
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c := Load(dir)
	c.Friends = []Friend{{Code: "xyzxyzxy", Name: "Beto"}}
	c.Exclude = []string{"discord.exe"}
	if err := Save(dir, c); err != nil {
		t.Fatal(err)
	}
	d := Load(dir)
	if d.MyCode != c.MyCode || len(d.Friends) != 1 || d.Friends[0].Name != "Beto" || d.Exclude[0] != "discord.exe" {
		t.Errorf("ida e volta perdeu dados: %+v", d)
	}
}

func TestNewCodeUsesUnambiguousAlphabet(t *testing.T) {
	for i := 0; i < 200; i++ {
		c := NewCode()
		if len(c) != 8 || strings.ContainsAny(c, "01ilo") {
			t.Fatalf("código ruim: %q", c)
		}
	}
}

func TestNormCode(t *testing.T) {
	cases := map[string]string{
		"  AbC-dEf 23 ": "abcdef23",
		"rzw3gr22":      "rzw3gr22",
		"çã!@#":         "",
	}
	for in, want := range cases {
		if got := NormCode(in); got != want {
			t.Errorf("NormCode(%q) = %q, quero %q", in, got, want)
		}
	}
}
