// pack comprime o ffmpeg.exe para ser embutido no tela.exe.
// Uso: go run ./cmd/pack <caminho/ffmpeg.exe> ffmpeg.exe.zst
package main

import (
	"io"
	"log"
	"os"

	"github.com/klauspost/compress/zstd"
)

func main() {
	if len(os.Args) != 3 {
		log.Fatal("uso: pack <ffmpeg.exe> <saida.zst>")
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
