package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		remote, current string
		want            bool
	}{
		{"0.2.1", "0.2.0", true},
		{"0.3.0", "0.2.9", true},
		{"0.2.10", "0.2.9", true}, // número, não texto
		{"1.0.0", "0.9.9", true},
		{"0.2.0", "0.2.0", false},
		{"0.1.9", "0.2.0", false},
		{"0.2.1", "", false}, // desenvolvimento: nunca atualiza
		{"lixo", "0.2.0", false},
	}
	for _, c := range cases {
		if got := newer(c.remote, c.current); got != c.want {
			t.Errorf("newer(%q, %q) = %v, quero %v", c.remote, c.current, got, c.want)
		}
	}
}

func TestParseRelease(t *testing.T) {
	js := `{"tag_name":"v0.2.1","assets":[
		{"name":"outra-coisa.zip","browser_download_url":"https://x/zip","size":1},
		{"name":"familiashare.exe","browser_download_url":"https://x/exe","size":62000000,"digest":"sha256:abc123"}]}`
	ver, a, err := parseRelease([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	if ver != "0.2.1" || a.url != "https://x/exe" || a.size != 62000000 || a.sha256 != "abc123" {
		t.Errorf("parseRelease = %q %+v", ver, a)
	}
	if _, _, err := parseRelease([]byte(`{"tag_name":"v0.2.1","assets":[]}`)); err == nil {
		t.Error("release sem o .exe deveria dar erro")
	}
	if _, _, err := parseRelease([]byte(`{"tag_name":"deps-ffmpeg-n8.1.3","assets":[{"name":"familiashare.exe"}]}`)); err == nil {
		t.Error("tag que não é versão deveria dar erro")
	}
}

func TestVerifyExe(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.exe")
	data := []byte("MZ resto do executável")
	os.WriteFile(p, data, 0o644)
	const sha = "deadbeef"
	ok := asset{size: int64(len(data)), sha256: sha}
	if err := verifyExe(p, ok, sha); err != nil {
		t.Errorf("arquivo certo foi recusado: %v", err)
	}
	if verifyExe(p, asset{size: 999}, sha) == nil {
		t.Error("tamanho diferente deveria ser recusado")
	}
	if verifyExe(p, ok, "outro") == nil {
		t.Error("SHA-256 diferente deveria ser recusado")
	}
	os.WriteFile(p, []byte("<html>"), 0o644)
	if verifyExe(p, asset{}, "") == nil {
		t.Error("página HTML não é executável")
	}
}
