// Comando packffmpeg comprime um ffmpeg.exe (zstd, nível máximo) para ser embutido
// no familiashare. Só é necessário ao trocar a versão do FFmpeg (ver CONTRIBUTING.md).
//
//	go run ./tools/packffmpeg C:\caminho\ffmpeg.exe internal\ffmpeg\ffmpeg.exe.zst
package main

import (
	"io"
	"log"
	"os"

	"github.com/klauspost/compress/zstd"
)

func main() {
	if len(os.Args) != 3 {
		log.Fatal("uso: packffmpeg <ffmpeg.exe> <saida.zst>")
	}
	in, err := os.Open(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(os.Args[2])
	if err != nil {
		log.Fatal(err)
	}
	enc, _ := zstd.NewWriter(out, zstd.WithEncoderLevel(zstd.SpeedBestCompression), zstd.WithWindowSize(64<<20))
	if _, err := io.Copy(enc, in); err != nil {
		log.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		log.Fatal(err)
	}
	out.Close()
}
