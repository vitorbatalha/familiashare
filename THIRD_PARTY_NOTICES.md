# Avisos de terceiros

O `familiashare.exe` inclui os componentes abaixo. Os textos completos das licenças estão
nos links.

## FFmpeg

- Versão: n8.1.3 (`n8.1.3-18-g46d899d7a0`), build `win64-lgpl` de
  [BtbN/FFmpeg-Builds](https://github.com/BtbN/FFmpeg-Builds)
- Licença: [GNU LGPL v3 ou posterior](https://www.gnu.org/licenses/lgpl-3.0.html)
  (configurado com `--enable-version3`, sem componentes GPL)
- Código-fonte: <https://github.com/FFmpeg/FFmpeg/tree/46d899d7a0> (branch `release/8.1`)
  e scripts de build em <https://github.com/BtbN/FFmpeg-Builds>

O FFmpeg é um programa separado: vai embutido (comprimido) no `.exe`, é extraído para
`%LOCALAPPDATA%\familiashare` e executado como processo à parte. Você pode substituí-lo
por outro build compatível. FFmpeg é uma marca de Fabrice Bellard, criador do projeto.

## Bibliotecas Go

| Biblioteca | Licença |
|---|---|
| [pion/webrtc](https://github.com/pion/webrtc) e demais módulos `github.com/pion/*` | MIT |
| [jchv/go-webview2](https://github.com/jchv/go-webview2) | MIT |
| [gorilla/websocket](https://github.com/gorilla/websocket) | BSD-2-Clause |
| [klauspost/compress](https://github.com/klauspost/compress) | BSD-3-Clause (partes Apache-2.0/MIT) |
| [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys), x/net, x/crypto | BSD-3-Clause |
| Biblioteca padrão do [Go](https://go.dev/LICENSE) | BSD-3-Clause |

A lista completa de módulos e versões está no [`go.mod`](go.mod).
