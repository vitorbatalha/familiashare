// Package update é a autoatualização do app pelos Releases do GitHub.
//
// O scripts/build.ps1 grava a versão (0.x.y) no programa (Version, via -ldflags). Ao
// abrir e a cada 6 h, o app pergunta à API do GitHub qual é o último release; se for
// mais novo, avisa. O botão "Atualizar" baixa o familiashare.exe do release, confere
// tamanho e SHA-256, troca o .exe e reabre o app.
//
// Até a 0.1.2 a atualização vinha de um link do Google Drive, lendo uma etiqueta no fim
// do .exe. O build ainda anexa essa etiqueta (ver build.ps1) para que essas versões
// antigas consigam atualizar para a 0.2.0 pelo Drive.
package update

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Version é definida no build (scripts/build.ps1):
//
//	-ldflags "-X github.com/vitorbatalha/familiashare/internal/update.Version=0.2.0"
//
// Vazia = build de desenvolvimento (não procura atualização).
var Version string

const (
	repo = "vitorbatalha/familiashare"
	// ReleasesURL é a página dos releases, para baixar manualmente pelo navegador.
	ReleasesURL   = "https://github.com/" + repo + "/releases/latest"
	latestAPI     = "https://api.github.com/repos/" + repo + "/releases/latest"
	assetName     = "familiashare.exe"
	checkInterval = 6 * time.Hour
)

// latestURL pode ser trocado por FAMILIASHARE_UPDATE_URL (só para testes locais): deve
// responder o mesmo JSON que a API do GitHub.
var latestURL = latestAPI

// State é o estado da atualização, para a interface.
type State struct {
	Available bool    `json:"available"`
	Version   string  `json:"version"`  // último release (ex.: "0.2.1")
	Current   string  `json:"current"`  // versão rodando ("" = desenvolvimento)
	Progress  float64 `json:"progress"` // 0..1 durante o download
	Busy      bool    `json:"busy"`
	Err       string  `json:"err"`
}

// release é o pedaço da resposta da API do GitHub que interessa.
type release struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		Size   int64  `json:"size"`
		Digest string `json:"digest"` // "sha256:…"
	} `json:"assets"`
}

// asset é o arquivo a baixar de um release.
type asset struct {
	url, sha256 string
	size        int64
}

// Updater procura e aplica atualizações.
type Updater struct {
	mu      sync.Mutex
	st      State
	asset   asset
	restart string // caminho do .exe novo, aberto quando o app terminar de fechar
	quit    func() // fecha a janela
}

// New começa a procurar atualizações (3 s depois de abrir e a cada 6 h). quit deve
// fechar a janela do app; depois que ela fechar, chame RestartIfNeeded.
func New(quit func()) *Updater {
	u := &Updater{st: State{Current: Version}, quit: quit}
	if t := os.Getenv("FAMILIASHARE_UPDATE_URL"); t != "" {
		latestURL = t
	}
	cleanupOldExe()
	if Version != "" {
		go func() {
			time.Sleep(3 * time.Second)
			for {
				u.check()
				time.Sleep(checkInterval)
			}
		}()
	}
	return u
}

func (u *Updater) State() State {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.st
}

func (u *Updater) check() {
	ver, a, err := latest()
	if err != nil {
		log.Printf("atualização: %v", err)
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.st.Available = newer(ver, Version)
	u.st.Version = ver
	u.asset = a
	log.Printf("atualização: rodando %s, último release %s", Version, ver)
}

// latest pergunta ao GitHub qual é o último release (pré-lançamentos, como o do
// FFmpeg, não contam).
func latest() (string, asset, error) {
	req, _ := http.NewRequest("GET", latestURL, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "familiashare/"+Version) // a API exige
	c := http.Client{Timeout: 20 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return "", asset{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", asset{}, fmt.Errorf("GitHub respondeu %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", asset{}, err
	}
	return parseRelease(b)
}

func parseRelease(b []byte) (string, asset, error) {
	var r release
	if err := json.Unmarshal(b, &r); err != nil {
		return "", asset{}, err
	}
	ver := strings.TrimPrefix(r.Tag, "v")
	if _, ok := parseVersion(ver); !ok {
		return "", asset{}, fmt.Errorf("release com versão inválida: %q", r.Tag)
	}
	for _, a := range r.Assets {
		if a.Name == assetName {
			return ver, asset{url: a.URL, size: a.Size, sha256: strings.TrimPrefix(a.Digest, "sha256:")}, nil
		}
	}
	return "", asset{}, fmt.Errorf("release %s sem %s", r.Tag, assetName)
}

// parseVersion lê "0.2.10" como [0 2 10].
func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// newer diz se remote é uma versão mais nova que current (comparando número a
// número: 0.2.10 é mais nova que 0.2.9).
func newer(remote, current string) bool {
	r, ok1 := parseVersion(remote)
	c, ok2 := parseVersion(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := range r {
		if r[i] != c[i] {
			return r[i] > c[i]
		}
	}
	return false
}

// Apply baixa a versão nova, troca o .exe e pede para o app fechar e reabrir.
func (u *Updater) Apply() {
	u.mu.Lock()
	if u.st.Busy || !u.st.Available {
		u.mu.Unlock()
		return
	}
	u.st.Busy, u.st.Err, u.st.Progress = true, "", 0
	a := u.asset
	u.mu.Unlock()

	newExe, err := u.download(a)
	u.mu.Lock()
	u.st.Busy = false
	if err != nil {
		u.st.Err = err.Error()
		u.mu.Unlock()
		log.Printf("atualização falhou: %v", err)
		return
	}
	u.restart = newExe
	u.mu.Unlock()
	u.quit()
}

func (u *Updater) download(a asset) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	tmp := self + ".new"
	f, err := os.Create(tmp)
	if err != nil {
		return "", fmt.Errorf("sem permissão para gravar na pasta do app (%v) — baixe pelo GitHub", err)
	}
	fail := func(err error) (string, error) {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	c := http.Client{Timeout: 15 * time.Minute}
	resp, err := c.Get(a.url)
	if err != nil {
		return fail(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fail(fmt.Errorf("GitHub respondeu %s", resp.Status))
	}
	h := sha256.New()
	var done int64
	buf := make([]byte, 256<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return fail(werr)
			}
			h.Write(buf[:n])
			done += int64(n)
			if a.size > 0 {
				u.mu.Lock()
				u.st.Progress = float64(done) / float64(a.size)
				u.mu.Unlock()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fail(rerr)
		}
	}
	f.Close()
	if err := verifyExe(tmp, a, hex.EncodeToString(h.Sum(nil))); err != nil {
		os.Remove(tmp)
		return "", err
	}
	// Um .exe em uso não pode ser apagado, mas pode ser renomeado.
	old := self + ".old"
	os.Remove(old)
	if err := os.Rename(self, old); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("não foi possível substituir o app: %v", err)
	}
	if err := os.Rename(tmp, self); err != nil {
		os.Rename(old, self) // desfaz
		return "", fmt.Errorf("não foi possível substituir o app: %v", err)
	}
	return self, nil
}

// verifyExe confere que o arquivo baixado é o executável inteiro do release.
func verifyExe(path string, a asset, gotSHA string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, _ := f.Stat()
	head := make([]byte, 2)
	if _, err := io.ReadFull(f, head); err != nil || !bytes.Equal(head, []byte("MZ")) {
		return errors.New("o arquivo baixado não é um executável válido")
	}
	if a.size > 0 && st.Size() != a.size {
		return errors.New("o download veio incompleto")
	}
	if a.sha256 != "" && !strings.EqualFold(gotSHA, a.sha256) {
		return errors.New("o arquivo baixado não confere (SHA-256 diferente)")
	}
	return nil
}

// RestartIfNeeded abre a versão nova depois que o app terminou de fechar.
func (u *Updater) RestartIfNeeded() {
	u.mu.Lock()
	exe := u.restart
	u.mu.Unlock()
	if exe == "" {
		return
	}
	cmd := exec.Command(exe)
	cmd.Dir = filepath.Dir(exe)
	if err := cmd.Start(); err != nil {
		log.Printf("não foi possível reabrir: %v", err)
	}
}

// cleanupOldExe apaga o .exe antigo deixado por uma atualização anterior
// (pode levar alguns segundos até o processo antigo terminar de fechar).
func cleanupOldExe() {
	self, err := os.Executable()
	if err != nil {
		return
	}
	old := self + ".old"
	go func() {
		for i := 0; i < 20; i++ {
			if err := os.Remove(old); err == nil || os.IsNotExist(err) {
				return
			}
			time.Sleep(time.Second)
		}
	}()
}
