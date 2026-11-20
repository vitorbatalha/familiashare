package config

import (
	"fmt"
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

func TestThemeDefaultsAndKeepsKnown(t *testing.T) {
	if c := Load(t.TempDir()); c.Theme != "escuro" {
		t.Errorf("tema = %q, quero escuro", c.Theme)
	}
	for _, v := range Themes {
		c := Config{Theme: v}
		c.Normalize()
		if c.Theme != v {
			t.Errorf("tema %q virou %q", v, c.Theme)
		}
	}
	c := Config{Theme: "rosa-choque"}
	c.Normalize()
	if c.Theme != "escuro" {
		t.Errorf("tema desconhecido virou %q, quero escuro", c.Theme)
	}
}

func TestKeyIsGeneratedAndKept(t *testing.T) {
	dir := t.TempDir()
	c := Load(dir)
	if c.PrivateKey() == nil {
		t.Fatal("a primeira execução deveria gerar a chave")
	}
	Save(dir, c)
	if Load(dir).Key != c.Key {
		t.Error("a chave não pode mudar entre execuções (os amigos guardam a pública)")
	}
	// config de versão antiga, sem chave: ganha uma e mantém o código
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"v":2,"myCode":"abcdefgh"}`), 0o644)
	if old := Load(dir); old.PrivateKey() == nil || old.MyCode != "abcdefgh" {
		t.Errorf("config antiga deveria ganhar chave e manter o código: %+v", old)
	}
}

func TestInvalidFriendKeyIsDropped(t *testing.T) {
	c := Config{Friends: []Friend{{Code: "abcdefgh", Key: "lixo"}, {Code: "hjkmnpqr", Key: NewKey()}}}
	c.Normalize()
	if c.Friends[0].Key != "" {
		t.Error("chave inválida deveria ser apagada (o amigo volta a ser aceito até assinar)")
	}
	if c.Friends[1].Key == "" {
		t.Error("chave válida deveria ser mantida")
	}
}

func TestVolumesSurviveSaveLoad(t *testing.T) {
	dir := t.TempDir()
	c := Load(dir)
	if !c.SetVolume("abcdefgh", 35) || !c.SetVolume("hjkmnpqr", 0) {
		t.Fatal("SetVolume deveria guardar volumes novos")
	}
	if err := Save(dir, c); err != nil {
		t.Fatal(err)
	}
	got := Load(dir).Volumes
	if got["abcdefgh"] != 35 || got["hjkmnpqr"] != 0 || len(got) != 2 {
		t.Errorf("volumes depois de salvar e abrir de novo: %v", got)
	}
}

func TestSetVolume(t *testing.T) {
	var c Config
	if c.SetVolume("abcdefgh", 100) {
		t.Error("100% é o padrão: não deveria guardar nada")
	}
	c.SetVolume(" ABCD-EFGH ", 150) // código digitado de qualquer jeito e volume fora da faixa
	if len(c.Volumes) != 0 {
		t.Errorf("150%% vira 100%% (padrão) e não é guardado: %v", c.Volumes)
	}
	c.SetVolume("abcdefgh", -5)
	if v, ok := c.Volumes["abcdefgh"]; !ok || v != 0 {
		t.Errorf("volume negativo deveria virar 0: %v", c.Volumes)
	}
	if c.SetVolume("abcdefgh", 0) {
		t.Error("mesmo volume não é mudança")
	}
	if !c.SetVolume("abcdefgh", 100) || len(c.Volumes) != 0 {
		t.Errorf("voltar para 100%% apaga o volume guardado: %v", c.Volumes)
	}
	if c.SetVolume("", 50) {
		t.Error("código vazio não pode ser guardado")
	}
}

func TestSetVolumeLimit(t *testing.T) {
	var c Config
	for i := range MaxVolumes {
		if !c.SetVolume(fmt.Sprintf("p%07d", i), 50) {
			t.Fatalf("volume %d deveria caber", i)
		}
	}
	if c.SetVolume("novatoxx", 50) || len(c.Volumes) != MaxVolumes {
		t.Errorf("passou do limite de %d volumes", MaxVolumes)
	}
	if !c.SetVolume("p0000000", 10) { // quem já está guardado ainda pode mudar
		t.Error("deveria poder mudar o volume de quem já está guardado")
	}
}

func TestNormalizeDropsInvalidVolumes(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte(`{"v":2,"myCode":"abcdefgh","volumes":{"hjkmnpqr":40,"ABC":10,"rstuvwxy":100,"stuvwxyz":-3,"":5}}`), 0o644)
	got := Load(dir).Volumes
	if len(got) != 1 || got["hjkmnpqr"] != 40 {
		t.Errorf("só o volume válido deveria sobrar: %v", got)
	}
}
