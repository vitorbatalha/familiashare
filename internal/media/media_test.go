package media

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/jpeg"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestThemeImageReplacesOld(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	h1, err := s.SaveTheme("escuro", "back", testJPEG(t, 40, 30))
	if err != nil {
		t.Fatal(err)
	}
	h2, err := s.SaveTheme("escuro", "back", testJPEG(t, 50, 30))
	if err != nil || h2 == h1 {
		t.Fatalf("segunda imagem: %v %v", h2, err)
	}
	files, _ := os.ReadDir(filepath.Join(dir, "themes"))
	if len(files) != 1 || files[0].Name() != "escuro-back-"+h2+".jpg" {
		t.Errorf("deveria sobrar só a imagem nova: %v", files)
	}
	s.SaveTheme("outro", "back", testJPEG(t, 10, 10))
	if _, err := s.SaveTheme("escuro", "cover", testJPEG(t, 10, 10)); err == nil {
		t.Error("só existe imagem de fundo")
	}
	s.RemoveTheme("escuro", "")
	s.RemoveTheme("outro", "")
	if files, _ := os.ReadDir(filepath.Join(dir, "themes")); len(files) != 0 {
		t.Errorf("remover o tema deveria apagar as duas imagens: %v", files)
	}
}

func TestRejectsBadImages(t *testing.T) {
	s := Open(t.TempDir())
	if _, err := s.SaveTheme("escuro", "back", []byte("não é imagem")); err == nil {
		t.Error("deveria recusar o que não é JPEG")
	}
	if _, err := s.SaveTheme("../x", "back", testJPEG(t, 10, 10)); err == nil {
		t.Error("deveria recusar nome de tema estranho")
	}
	if _, err := s.SaveMine(testJPEG(t, 600, 600)); err == nil {
		t.Error("foto de perfil grande demais deveria ser recusada")
	}
	b := testJPEG(t, 64, 64)
	if err := s.SaveAvatar("betoooo2", "0000000000000000", b); err == nil {
		t.Error("foto que não confere com o hash deveria ser recusada")
	}
	if _, err := DecodeDataURL("data:image/png;base64,AAAA"); err == nil {
		t.Error("só JPEG")
	}
	if got, err := DecodeDataURL("data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b)); err != nil || !bytes.Equal(got, b) {
		t.Errorf("data URL: %v", err)
	}
}

func TestAvatarsSurviveRestartAndAreServed(t *testing.T) {
	dir := t.TempDir()
	b := testJPEG(t, 64, 64)
	h := Hash(b)
	if err := Open(dir).SaveAvatar("betoooo2", h, b); err != nil {
		t.Fatal(err)
	}
	s := Open(dir) // app reaberto
	if s.AvatarHash("betoooo2") != h {
		t.Fatal("a foto guardada deveria ser encontrada ao abrir de novo")
	}
	url := s.AvatarURLs()["betoooo2"]
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), b) {
		t.Errorf("GET %s = %d", url, rec.Code)
	}
	for _, bad := range []string{"/u/a/../config.json", "/u/x/a.jpg", "/u/m/config.jpg"} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", bad, nil))
		if rec.Code == 200 {
			t.Errorf("%s não deveria ser servido", bad)
		}
	}
	s.RemoveAvatar("betoooo2")
	if s.AvatarHash("betoooo2") != "" {
		t.Error("foto removida")
	}
}
