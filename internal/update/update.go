// Package update é a autoatualização do app a partir do link público do Google Drive.
//
// O scripts/build.ps1 grava um identificador de versão (data/hora do build) em dois lugares:
//   - dentro do programa (BuildID, via -ldflags), e
//   - nos últimos bytes do familiashare.exe (uma "etiqueta" anexada ao arquivo).
//
// Ao abrir, o app pede ao Drive só os últimos 64 bytes do arquivo publicado e compara
// a etiqueta com o próprio BuildID. Se a do Drive for mais nova, avisa; o botão
// "Atualizar" baixa o arquivo inteiro, troca o .exe e reabre o app.
package update

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// BuildID é definido no build (scripts/build.ps1):
//
//	-ldflags "-X github.com/vitorbatalha/familiashare/internal/update.BuildID=20261029-141500"
//
// Vazio = build de desenvolvimento (não procura atualização).
var BuildID string

const (
	driveFileID = "1FTrDu4b9hTEYuy83MQdDJquKykb2QX4C"
	// confirm=t pula a página "não foi possível verificar vírus" que o Drive mostra para .exe
	driveDownloadURL = "https://drive.usercontent.google.com/download?id=" + driveFileID + "&export=download&confirm=t"
	// DriveViewURL é a página do arquivo, para baixar manualmente pelo navegador.
	DriveViewURL  = "https://drive.google.com/file/d/" + driveFileID + "/view"
	trailerMagic  = "FAMILIASHARE-BUILD:"
	checkInterval = 6 * time.Hour
)

// driveURL pode ser trocado por FAMILIASHARE_UPDATE_URL (só para testes locais).
var driveURL = driveDownloadURL

var trailerRe = regexp.MustCompile(trailerMagic + `([0-9]{8}-[0-9]{6})`)

// State é o estado da atualização, para a interface.
type State struct {
	Available bool    `json:"available"`
	Version   string  `json:"version"`  // versão no Drive
	Current   string  `json:"current"`  // versão rodando
	Progress  float64 `json:"progress"` // 0..1 durante o download
	Busy      bool    `json:"busy"`
	Err       string  `json:"err"`
}

// Updater procura e aplica atualizações.
type Updater struct {
	mu      sync.Mutex
	st      State
	restart string // caminho do .exe novo, aberto quando o app terminar de fechar
	quit    func() // fecha a janela
}

// New começa a procurar atualizações (3 s depois de abrir e a cada 6 h). quit deve
// fechar a janela do app; depois que ela fechar, chame RestartIfNeeded.
func New(quit func()) *Updater {
	u := &Updater{st: State{Current: BuildID}, quit: quit}
	if t := os.Getenv("FAMILIASHARE_UPDATE_URL"); t != "" {
		driveURL = t
	}
	cleanupOldExe()
	if BuildID != "" {
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

// check lê só a etiqueta no fim do arquivo do Drive (pedido de 64 bytes).
func (u *Updater) check() {
	remote, err := remoteBuildID()
	if err != nil {
		log.Printf("atualização: %v", err)
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.st.Available = newer(remote, BuildID)
	u.st.Version = remote
	log.Printf("atualização: rodando %s, Drive tem %s", BuildID, remote)
}

func remoteBuildID() (string, error) {
	req, _ := http.NewRequest("GET", driveURL, nil)
	req.Header.Set("Range", "bytes=-64")
	c := http.Client{Timeout: 20 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return "", fmt.Errorf("Drive respondeu %s (esperado 206)", resp.Status)
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	id, ok := parseTrailer(b)
	if !ok {
		return "", errors.New("arquivo no Drive sem etiqueta de versão (versão antiga)")
	}
	return id, nil
}

// parseTrailer acha a etiqueta "FAMILIASHARE-BUILD:aaaammdd-hhmmss" em b.
func parseTrailer(b []byte) (string, bool) {
	m := trailerRe.FindSubmatch(b)
	if m == nil {
		return "", false
	}
	return string(m[1]), true
}

// newer diz se remote é uma versão mais nova que current. Os ids são data-hora
// (aaaammdd-hhmmss), então comparar o texto é comparar a data.
func newer(remote, current string) bool { return remote > current }

// Apply baixa a versão nova, troca o .exe e pede para o app fechar e reabrir.
func (u *Updater) Apply() {
	u.mu.Lock()
	if u.st.Busy || !u.st.Available {
		u.mu.Unlock()
		return
	}
	u.st.Busy, u.st.Err, u.st.Progress = true, "", 0
	want := u.st.Version
	u.mu.Unlock()

	newExe, err := u.download(want)
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

func (u *Updater) download(want string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	tmp := self + ".new"
	f, err := os.Create(tmp)
	if err != nil {
		return "", fmt.Errorf("sem permissão para gravar na pasta do app (%v) — baixe pelo Drive", err)
	}
	c := http.Client{Timeout: 15 * time.Minute}
	resp, err := c.Get(driveURL)
	if err != nil {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		f.Close()
		os.Remove(tmp)
		return "", fmt.Errorf("Drive respondeu %s", resp.Status)
	}
	total := resp.ContentLength
	var done int64
	buf := make([]byte, 256<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				os.Remove(tmp)
				return "", werr
			}
			done += int64(n)
			if total > 0 {
				u.mu.Lock()
				u.st.Progress = float64(done) / float64(total)
				u.mu.Unlock()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			os.Remove(tmp)
			return "", rerr
		}
	}
	f.Close()
	if err := verifyExe(tmp, want); err != nil {
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

// verifyExe confere que o arquivo baixado é um executável inteiro da versão esperada
// (e não, por exemplo, uma página de aviso do Drive).
func verifyExe(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, _ := f.Stat()
	head := make([]byte, 2)
	if _, err := io.ReadFull(f, head); err != nil || !bytes.Equal(head, []byte("MZ")) || st.Size() < 10<<20 {
		return errors.New("o arquivo baixado não é um executável válido")
	}
	tail := make([]byte, 64)
	f.ReadAt(tail, st.Size()-64)
	m := trailerRe.FindSubmatch(tail)
	if m == nil || string(m[1]) != want {
		return errors.New("o arquivo baixado está incompleto ou é de outra versão")
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
