package config

// Aparência: temas criados pela pessoa, imagens de cada tema e foto de perfil.

import (
	"crypto/rand"
	"regexp"
	"slices"
	"strings"
)

// CustomTheme é um tema criado pela pessoa. Ela escolhe só 4 cores (#rrggbb); as
// outras (bordas, cartões, texto fraco...) a página calcula a partir delas.
type CustomTheme struct {
	ID     string `json:"id"` // "meu-" + 6 letras/números
	Name   string `json:"name"`
	Bg     string `json:"bg"`     // fundo
	Side   string `json:"side"`   // lateral
	Fg     string `json:"fg"`     // texto
	Accent string `json:"accent"` // botão principal
}

// ThemeImage é a imagem de fundo (atrás de todo o app) de um tema criado pela pessoa. O
// arquivo fica na pasta do app (ver internal/media); aqui fica só o hash dele e os ajustes.
type ThemeImage struct {
	Back string `json:"back,omitempty"`
	Blur int    `json:"blur"` // desfoque, 0..20 px
	Dim  int    `json:"dim"`  // escurecer, 0..90 %
	PosX int    `json:"posX"` // parte da imagem que aparece, 0..100 % (50 = centro)
	PosY int    `json:"posY"`
	Zoom int    `json:"zoom"` // 100..300 % sobre o tamanho que cobre a janela (0 = 100, configs antigas)
}

// MaxMyThemes limita quantos temas a pessoa pode criar.
const MaxMyThemes = 20

var (
	reThemeID = regexp.MustCompile(`^meu-[a-z0-9]{6}$`)
	reHex     = regexp.MustCompile(`^#[0-9a-f]{6}$`)
	reHash    = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

// IsHash diz se h tem o formato dos hashes de imagem (16 hexadecimais).
func IsHash(h string) bool { return reHash.MatchString(h) }

// IsTheme diz se id é um tema padrão ou um tema criado que existe.
func (c *Config) IsTheme(id string) bool {
	return slices.Contains(Themes, id) || c.myTheme(id) >= 0
}

func (c *Config) myTheme(id string) int {
	return slices.IndexFunc(c.MyThemes, func(t CustomTheme) bool { return t.ID == id })
}

// cleanTheme valida e arruma um tema criado; devolve false se não dá para usar.
func cleanTheme(t *CustomTheme) bool {
	t.Name = strings.TrimSpace(t.Name)
	if r := []rune(t.Name); len(r) > 24 {
		t.Name = string(r[:24])
	}
	if t.Name == "" {
		t.Name = "Meu tema"
	}
	for _, p := range []*string{&t.Bg, &t.Side, &t.Fg, &t.Accent} {
		*p = strings.ToLower(strings.TrimSpace(*p))
		if !reHex.MatchString(*p) {
			return false
		}
	}
	return true
}

// SaveTheme cria (ID vazio) ou atualiza um tema da pessoa. Devolve o ID, ou "" se as
// cores forem inválidas, o tema não existir ou já houver MaxMyThemes.
func (c *Config) SaveTheme(t CustomTheme) string {
	if !cleanTheme(&t) {
		return ""
	}
	if t.ID == "" {
		if len(c.MyThemes) >= MaxMyThemes {
			return ""
		}
		for t.ID == "" || c.myTheme(t.ID) >= 0 {
			t.ID = "meu-" + randomID(6)
		}
		c.MyThemes = append(c.MyThemes, t)
		return t.ID
	}
	i := c.myTheme(t.ID)
	if i < 0 {
		return ""
	}
	c.MyThemes[i] = t
	return t.ID
}

// DeleteTheme apaga um tema criado (os padrão não podem ser apagados) e as imagens dele.
// Se era o tema em uso, volta para o padrão. Devolve se apagou.
func (c *Config) DeleteTheme(id string) bool {
	i := c.myTheme(id)
	if i < 0 {
		return false
	}
	c.MyThemes = slices.Delete(c.MyThemes, i, i+1)
	delete(c.Images, id)
	if c.Theme == id {
		c.Theme = Themes[0]
	}
	return true
}

// SetThemeImage troca a imagem de fundo ("back") de um tema; hash "" remove.
func (c *Config) SetThemeImage(theme, slot, hash string) bool {
	if c.myTheme(theme) < 0 || slot != "back" || (hash != "" && !IsHash(hash)) { // só temas criados têm imagem
		return false
	}
	im := c.Images[theme]
	if im.Back == "" && hash != "" { // imagem nova: centralizada
		im.PosX, im.PosY, im.Zoom = 50, 50, 120 // com um pouco de zoom dá para mover nos dois sentidos
	}
	if im.Back == "" && hash != "" && im.Blur == 0 && im.Dim == 0 {
		im.Blur, im.Dim = 8, 40 // texto legível desde o começo
	}
	im.Back = hash
	if c.Images == nil {
		c.Images = map[string]ThemeImage{}
	}
	c.Images[theme] = im
	c.cleanImages()
	return true
}

// SetThemeImageOpts troca os ajustes (desfoque, escurecer, posição e zoom) da imagem de um tema.
func (c *Config) SetThemeImageOpts(theme string, o ThemeImage) bool {
	im, ok := c.Images[theme]
	if !ok {
		return false
	}
	im.Blur, im.Dim, im.PosX, im.PosY, im.Zoom = o.Blur, o.Dim, o.PosX, o.PosY, o.Zoom
	c.Images[theme] = im
	c.cleanImages()
	return true
}

func (c *Config) cleanImages() {
	for id, im := range c.Images {
		if c.myTheme(id) < 0 {
			delete(c.Images, id)
			continue
		}
		if !IsHash(im.Back) {
			delete(c.Images, id)
			continue
		}
		im.Blur = max(0, min(20, im.Blur))
		im.Dim = max(0, min(90, im.Dim))
		im.PosX = max(0, min(100, im.PosX))
		im.PosY = max(0, min(100, im.PosY))
		im.Zoom = max(100, min(300, im.Zoom))
		c.Images[id] = im
	}
}

// normalizeLook arruma temas, imagens e foto de uma config lida do disco.
func (c *Config) normalizeLook() {
	seen := map[string]bool{}
	c.MyThemes = slices.DeleteFunc(c.MyThemes, func(t CustomTheme) bool {
		bad := !reThemeID.MatchString(t.ID) || seen[t.ID]
		seen[t.ID] = true
		return bad
	})
	for i := range c.MyThemes {
		if !cleanTheme(&c.MyThemes[i]) {
			c.MyThemes[i].ID = "" // marcado para sair abaixo
		}
	}
	c.MyThemes = slices.DeleteFunc(c.MyThemes, func(t CustomTheme) bool { return t.ID == "" })
	if len(c.MyThemes) > MaxMyThemes {
		c.MyThemes = c.MyThemes[:MaxMyThemes]
	}
	if !c.IsTheme(c.Theme) {
		c.Theme = Themes[0]
	}
	c.cleanImages()
	if !IsHash(c.Avatar) {
		c.Avatar = ""
	}
}

func randomID(n int) string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, n)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}
