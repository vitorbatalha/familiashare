package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMyThemes(t *testing.T) {
	var c Config
	id := c.SaveTheme(CustomTheme{Name: "  Pôr do sol  ", Bg: "#1F0F18", Side: "#2a1420", Fg: "#ffe9d6", Accent: "#ff8a5c"})
	if !reThemeID.MatchString(id) || c.MyThemes[0].Name != "Pôr do sol" || c.MyThemes[0].Bg != "#1f0f18" {
		t.Fatalf("tema criado: %q %+v", id, c.MyThemes)
	}
	if c.SaveTheme(CustomTheme{Name: "x", Bg: "vermelho", Side: "#000000", Fg: "#ffffff", Accent: "#ffffff"}) != "" {
		t.Error("cor inválida não pode ser salva")
	}
	c.MyThemes[0].Name = "Outro"
	if c.SaveTheme(CustomTheme{ID: id, Name: "Novo nome", Bg: "#000000", Side: "#000000", Fg: "#ffffff", Accent: "#ffffff"}) != id || c.MyThemes[0].Name != "Novo nome" || len(c.MyThemes) != 1 {
		t.Errorf("editar deveria manter o ID: %+v", c.MyThemes)
	}
	if c.SaveTheme(CustomTheme{ID: "meu-naoexi", Bg: "#000000", Side: "#000000", Fg: "#ffffff", Accent: "#ffffff"}) != "" {
		t.Error("editar um tema que não existe")
	}
	for len(c.MyThemes) < MaxMyThemes {
		c.SaveTheme(CustomTheme{Bg: "#000000", Side: "#000000", Fg: "#ffffff", Accent: "#ffffff"})
	}
	if c.SaveTheme(CustomTheme{Bg: "#000000", Side: "#000000", Fg: "#ffffff", Accent: "#ffffff"}) != "" {
		t.Errorf("passou do limite de %d temas", MaxMyThemes)
	}
}

func TestDeleteThemeAndImages(t *testing.T) {
	var c Config
	id := c.SaveTheme(CustomTheme{Bg: "#000000", Side: "#000000", Fg: "#ffffff", Accent: "#ffffff"})
	c.Theme = id
	if !c.SetThemeImage(id, "back", "0123456789abcdef") || c.Images[id].Blur != 8 || c.Images[id].Dim != 40 {
		t.Fatalf("fundo novo deveria vir com desfoque e escurecimento: %+v", c.Images)
	}
	if c.SetThemeImage("escuro", "back", "fedcba9876543210") {
		t.Error("temas padrão não têm imagens")
	}
	outro := c.SaveTheme(CustomTheme{Bg: "#111111", Side: "#000000", Fg: "#ffffff", Accent: "#ffffff"})
	c.SetThemeImage(outro, "back", "fedcba9876543210")
	if c.DeleteTheme("escuro") {
		t.Error("tema padrão não pode ser apagado")
	}
	if !c.DeleteTheme(id) || c.Theme != "escuro" || len(c.MyThemes) != 1 {
		t.Errorf("apagar o tema em uso volta para o padrão: %q %+v", c.Theme, c.MyThemes)
	}
	if _, ok := c.Images[id]; ok {
		t.Error("as imagens do tema apagado também saem")
	}
	if c.Images[outro].Back == "" {
		t.Error("as imagens dos outros temas ficam")
	}
	if c.SetThemeImage(outro, "back", "../../x") || c.SetThemeImage(outro, "cover", "fedcba9876543210") {
		t.Error("hash inválido")
	}
}

func TestNormalizeLook(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"v":2,"myCode":"abcdefgh","theme":"meu-abc234",
		"myThemes":[{"id":"meu-abc234","name":"Bom","bg":"#000000","side":"#111111","fg":"#ffffff","accent":"#ff0000"},
		            {"id":"meu-abc234","name":"Repetido","bg":"#000000","side":"#111111","fg":"#ffffff","accent":"#ff0000"},
		            {"id":"x","name":"ID ruim","bg":"#000000","side":"#111111","fg":"#ffffff","accent":"#ff0000"},
		            {"id":"meu-zzzzzz","name":"Cor ruim","bg":"preto","side":"#111111","fg":"#ffffff","accent":"#ff0000"}],
		"themeImages":{"meu-abc234":{"back":"0123456789abcdef","blur":99},"sumiu":{"back":"0123456789abcdef"},"escuro":{"back":"0123456789abcdef"}},
		"avatar":"nao-e-hash"}`), 0o644)
	c := Load(dir)
	if len(c.MyThemes) != 1 || c.MyThemes[0].Name != "Bom" || c.Theme != "meu-abc234" {
		t.Errorf("só o tema válido deveria sobrar: %+v (tema %q)", c.MyThemes, c.Theme)
	}
	if len(c.Images) != 1 || c.Images["meu-abc234"].Blur != 20 {
		t.Errorf("imagens de tema que não existe saem e os ajustes ficam na faixa: %+v", c.Images)
	}
	if c.Avatar != "" {
		t.Error("hash de foto inválido")
	}
}

func TestThemeImagePosition(t *testing.T) {
	var c Config
	id := c.SaveTheme(CustomTheme{Bg: "#000000", Side: "#000000", Fg: "#ffffff", Accent: "#ffffff"})
	c.SetThemeImage(id, "back", "0123456789abcdef")
	if im := c.Images[id]; im.PosX != 50 || im.PosY != 50 || im.Zoom != 120 {
		t.Errorf("imagem nova começa centralizada: %+v", im)
	}
	c.SetThemeImageOpts(id, ThemeImage{Blur: 4, Dim: 30, PosX: 150, PosY: -3, Zoom: 900})
	if im := c.Images[id]; im.PosX != 100 || im.PosY != 0 || im.Zoom != 300 || im.Blur != 4 || im.Back == "" {
		t.Errorf("posição fica entre 0 e 100 e a imagem continua: %+v", im)
	}
}
