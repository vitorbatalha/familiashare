// Package ffmpeg cuida do FFmpeg embutido no app: extrai o executável na pasta
// de dados, cria comandos e descobre quais encoders de hardware funcionam aqui.
//
// O arquivo ffmpeg.exe.zst (FFmpeg "lgpl" do BtbN, comprimido com zstd) não é
// versionado: scripts/fetch-ffmpeg.ps1 o baixa antes do build.
package ffmpeg

import (
	"bytes"
	_ "embed"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/klauspost/compress/zstd"
	"github.com/vitorbatalha/familiashare/internal/winapi"
)

//go:embed ffmpeg.exe.zst
var compressed []byte

var (
	dir      string
	once     sync.Once
	exePath  string
	extractE error
)

// Init define a pasta onde o FFmpeg será extraído (a pasta de dados do app).
func Init(dataDir string) { dir = dataDir }

// Path extrai o FFmpeg na primeira chamada (≈1 s) e devolve o caminho do executável.
// O nome inclui um checksum, então trocar o FFmpeg embutido gera uma nova extração.
func Path() (string, error) {
	once.Do(func() {
		name := fmt.Sprintf("ffmpeg-%08x.exe", crc32.ChecksumIEEE(compressed))
		exePath = filepath.Join(dir, name)
		if st, err := os.Stat(exePath); err == nil && st.Size() > 0 {
			return
		}
		extractE = extract(exePath)
		// remove versões antigas
		old, _ := filepath.Glob(filepath.Join(dir, "ffmpeg-*.exe"))
		for _, o := range old {
			if o != exePath {
				os.Remove(o)
			}
		}
	})
	return exePath, extractE
}

func extract(dst string) error {
	dec, err := zstd.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return err
	}
	defer dec.Close()
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, dec)
	f.Close()
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// Command monta um comando FFmpeg silencioso, sem janela de console.
func Command(args ...string) (*exec.Cmd, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(p, append([]string{"-hide_banner", "-nostdin", "-loglevel", "error"}, args...)...)
	winapi.HideWindow(cmd)
	return cmd, nil
}

// Families são as famílias de encoder suportadas, em ordem de preferência.
var Families = []string{"nvenc", "amf", "qsv", "mf"}

var (
	encOnce  sync.Once
	encAvail map[string]bool
)

// Available testa (uma vez, em paralelo) quais encoders funcionam nesta máquina.
// Chaves no formato do FFmpeg: "h264_nvenc", "av1_amf"...
func Available() map[string]bool {
	encOnce.Do(func() {
		encAvail = map[string]bool{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, fam := range Families {
			for _, codec := range []string{"h264", "av1"} {
				name := codec + "_" + fam
				wg.Add(1)
				go func() {
					defer wg.Done()
					cmd, err := Command("-f", "lavfi", "-i", "color=black:s=1280x720:r=30", "-frames:v", "3", "-c:v", name, "-f", "null", "-")
					if err == nil && cmd.Run() == nil {
						mu.Lock()
						encAvail[name] = true
						mu.Unlock()
					}
				}()
			}
		}
		wg.Wait()
	})
	return encAvail
}
