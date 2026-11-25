// Package media guarda as imagens do app na pasta de dados e as serve para a página:
//
//	themes\<tema>-<capa|fundo>-<hash>.jpg   imagens dos temas (só deste PC)
//	avatar-<hash>.jpg                       a minha foto de perfil
//	avatars\<código>-<hash>.jpg             fotos dos amigos (recebidas pela rede)
//
// O hash (16 hexadecimais do SHA-256) vai no nome do arquivo e na URL, então trocar uma
// imagem muda a URL e a página nunca mostra a antiga do cache.
package media

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Limites das imagens. A foto de perfil vai pela sinalização para os amigos, então
// precisa ser pequena (a página a reduz para 128×128).
const (
	MaxThemeBytes  = 8 << 20
	MaxThemeSide   = 4096
	MaxAvatarBytes = 24 << 10
	MaxAvatarSide  = 256
)

// Hash é o identificador de uma imagem: os 16 primeiros hexadecimais do SHA-256.
func Hash(b []byte) string {
	s := sha256.Sum256(b)
	return fmt.Sprintf("%x", s[:8])
}

// DecodeDataURL lê "data:image/jpeg;base64,..." (o que a página manda).
func DecodeDataURL(s string) ([]byte, error) {
	i := strings.Index(s, ";base64,")
	if !strings.HasPrefix(s, "data:image/jpeg") || i < 0 {
		return nil, errors.New("formato de imagem inesperado")
	}
	return base64.StdEncoding.DecodeString(s[i+len(";base64,"):])
}

// CheckJPEG confere se b é um JPEG dentro dos limites (não confia em quem enviou).
func CheckJPEG(b []byte, maxBytes, maxSide int) error {
	if len(b) == 0 || len(b) > maxBytes {
		return fmt.Errorf("imagem grande demais (%d KB)", len(b)>>10)
	}
	c, err := jpeg.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return errors.New("a imagem não é um JPEG válido")
	}
	if c.Width < 1 || c.Height < 1 || c.Width > maxSide || c.Height > maxSide {
		return fmt.Errorf("imagem com tamanho inválido (%d×%d)", c.Width, c.Height)
	}
	return nil
}

var (
	reCode  = regexp.MustCompile(`^[a-z0-9]{1,16}$`)
	reTheme = regexp.MustCompile(`^[a-z0-9-]{1,20}$`)
	reHash  = regexp.MustCompile(`^[0-9a-f]{16}$`)
	reFile  = regexp.MustCompile(`^[a-z0-9-]{1,48}\.jpg$`)
)

// Store guarda as imagens em dir.
type Store struct {
	dir     string
	mu      sync.Mutex
	avatars map[string]string // código → hash da foto guardada
}

// Open abre (e cria) as pastas de imagens em dir.
func Open(dir string) *Store {
	s := &Store{dir: dir, avatars: map[string]string{}}
	os.MkdirAll(filepath.Join(dir, "themes"), 0o755)
	os.MkdirAll(filepath.Join(dir, "avatars"), 0o755)
	files, _ := os.ReadDir(filepath.Join(dir, "avatars"))
	for _, f := range files {
		code, hash, ok := strings.Cut(strings.TrimSuffix(f.Name(), ".jpg"), "-")
		if ok && reCode.MatchString(code) && reHash.MatchString(hash) {
			if old, dup := s.avatars[code]; dup { // sobrou de uma troca interrompida
				os.Remove(filepath.Join(dir, "avatars", code+"-"+old+".jpg"))
			}
			s.avatars[code] = hash
		}
	}
	return s
}

// removeMatching apaga os arquivos de sub cujo nome começa com prefix, menos keep.
func (s *Store) removeMatching(sub, prefix, keep string) {
	files, _ := os.ReadDir(filepath.Join(s.dir, sub))
	for _, f := range files {
		if strings.HasPrefix(f.Name(), prefix) && f.Name() != keep {
			os.Remove(filepath.Join(s.dir, sub, f.Name()))
		}
	}
}

/* ---------- Imagens dos temas ---------- */

// SaveTheme guarda a imagem de fundo (slot "back") do tema e apaga a anterior.
func (s *Store) SaveTheme(theme, slot string, b []byte) (string, error) {
	if !reTheme.MatchString(theme) || slot != "back" {
		return "", errors.New("tema inválido")
	}
	if err := CheckJPEG(b, MaxThemeBytes, MaxThemeSide); err != nil {
		return "", err
	}
	h := Hash(b)
	name := theme + "-" + slot + "-" + h + ".jpg"
	if err := os.WriteFile(filepath.Join(s.dir, "themes", name), b, 0o644); err != nil {
		return "", err
	}
	s.removeMatching("themes", theme+"-"+slot+"-", name)
	return h, nil
}

// RemoveTheme apaga a imagem slot do tema ("" = as duas).
func (s *Store) RemoveTheme(theme, slot string) {
	if !reTheme.MatchString(theme) {
		return
	}
	prefix := theme + "-"
	if slot != "" {
		prefix += slot + "-"
	}
	s.removeMatching("themes", prefix, "")
}

/* ---------- Foto de perfil ---------- */

// SaveMine guarda a minha foto e apaga a anterior.
func (s *Store) SaveMine(b []byte) (string, error) {
	if err := CheckJPEG(b, MaxAvatarBytes, MaxAvatarSide); err != nil {
		return "", err
	}
	h := Hash(b)
	name := "avatar-" + h + ".jpg"
	if err := os.WriteFile(filepath.Join(s.dir, name), b, 0o644); err != nil {
		return "", err
	}
	s.removeMatching("", "avatar-", name)
	return h, nil
}

// RemoveMine apaga a minha foto.
func (s *Store) RemoveMine() { s.removeMatching("", "avatar-", "") }

// Mine lê a minha foto com esse hash.
func (s *Store) Mine(hash string) ([]byte, error) {
	if !reHash.MatchString(hash) {
		return nil, errors.New("hash inválido")
	}
	return os.ReadFile(filepath.Join(s.dir, "avatar-"+hash+".jpg"))
}

// MineURL é a URL da minha foto para a página ("" = sem foto).
func (s *Store) MineURL(hash string) string {
	if !reHash.MatchString(hash) {
		return ""
	}
	return "/u/m/avatar-" + hash + ".jpg"
}

/* ---------- Fotos dos amigos ---------- */

// AvatarHash é o hash da foto guardada de code ("" = nenhuma).
func (s *Store) AvatarHash(code string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.avatars[code]
}

// SaveAvatar guarda a foto de code recebida pela rede, conferindo hash e formato.
func (s *Store) SaveAvatar(code, hash string, b []byte) error {
	if !reCode.MatchString(code) || !reHash.MatchString(hash) {
		return errors.New("código ou hash inválido")
	}
	if Hash(b) != hash {
		return errors.New("a foto não confere com o hash")
	}
	if err := CheckJPEG(b, MaxAvatarBytes, MaxAvatarSide); err != nil {
		return err
	}
	name := code + "-" + hash + ".jpg"
	if err := os.WriteFile(filepath.Join(s.dir, "avatars", name), b, 0o644); err != nil {
		return err
	}
	s.removeMatching("avatars", code+"-", name)
	s.mu.Lock()
	s.avatars[code] = hash
	s.mu.Unlock()
	return nil
}

// RemoveAvatar apaga a foto guardada de code (a pessoa tirou a foto).
func (s *Store) RemoveAvatar(code string) {
	if !reCode.MatchString(code) {
		return
	}
	s.mu.Lock()
	_, had := s.avatars[code]
	delete(s.avatars, code)
	s.mu.Unlock()
	if had {
		s.removeMatching("avatars", code+"-", "")
	}
}

// AvatarURLs são as URLs das fotos guardadas, por código.
func (s *Store) AvatarURLs() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.avatars))
	for code, h := range s.avatars {
		out[code] = "/u/a/" + code + "-" + h + ".jpg"
	}
	return out
}

/* ---------- Servidor para a página ---------- */

// Handler serve as imagens em /u/t/ (temas), /u/a/ (amigos) e /u/m/ (minha foto).
// Só nomes no formato esperado; nada fora das pastas de imagens.
func (s *Store) Handler() http.Handler {
	dirs := map[string]string{"t": "themes", "a": "avatars", "m": ""}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind, name, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/u/"), "/")
		sub, known := dirs[kind]
		if !ok || !known || !reFile.MatchString(name) || (kind == "m" && !strings.HasPrefix(name, "avatar-")) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "max-age=31536000, immutable") // o hash está no nome
		http.ServeFile(w, r, filepath.Join(s.dir, sub, name))
	})
}
