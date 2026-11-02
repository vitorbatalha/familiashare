# Contribuindo

Obrigado por ajudar! Este guia cobre como montar o ambiente, compilar, testar e enviar
mudanças. Para entender como o app funciona por dentro, leia antes o
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Ambiente

O app só roda no **Windows 10 (2004+) ou 11** (usa APIs do Windows para captura de tela,
áudio e janela).

1. **Go** na versão do `go.mod` ou mais nova: `winget install GoLang.Go`
2. **Git**: `winget install Git.Git`
3. Clone e baixe o FFmpeg (≈50 MB, conferido por checksum; não fica no git):

   ```powershell
   git clone https://github.com/vitorbatalha/familiashare.git
   cd familiashare
   .\scripts\fetch-ffmpeg.ps1
   ```

## Compilar e rodar

```powershell
.\scripts\build.ps1           # dist\familiashare.exe de desenvolvimento
.\dist\familiashare.exe
```

O build de desenvolvimento não tem versão e não procura atualizações. Builds com
versão saem do GitHub (ver [Lançando uma versão](#lançando-uma-versão)).

O log fica em `%LOCALAPPDATA%\familiashare\familiashare.log` e, se o app fechar sozinho,
o motivo vai para `crash.log` na mesma pasta.

### Testar com várias pessoas no mesmo PC

Cada instância pode ter a sua própria identidade (pasta de dados) e título de janela:

```powershell
$env:FAMILIASHARE_DATA = "$env:TEMP\fs-ana";  $env:FAMILIASHARE_TITLE = "familiashare - Ana";  Start-Process .\dist\familiashare.exe
$env:FAMILIASHARE_DATA = "$env:TEMP\fs-beto"; $env:FAMILIASHARE_TITLE = "familiashare - Beto"; Start-Process .\dist\familiashare.exe
```

Adicione uma como amiga da outra, abra uma sala e transmita/assista entre elas. Lembre que
a transmissão captura a **sua tela de verdade**.

Outras variáveis úteis:

| Variável | Para quê |
|---|---|
| `FAMILIASHARE_DEBUG=1` | habilita as ferramentas de desenvolvedor do WebView2 (clique direito → Inspecionar) |
| `FAMILIASHARE_AUDIOTEST=1` | mede o volume capturado a cada segundo (com `FAMILIASHARE_EXCLUDE=discord` testa a exclusão de apps) |
| `FAMILIASHARE_BENCH=20` | benchmark de transmissão (ver abaixo) |
| `FAMILIASHARE_UPDATE_URL` | troca a API de releases do GitHub por outro endereço que responda o mesmo JSON (para testar a atualização com um build `-Version 0.0.1`) |

## Testes

```powershell
go test ./...
```

Os testes não precisam de rede, GPU nem janela. Destaques:

- `internal/social`: vários apps ligados por um servidor de sinalização falso, em memória
  (amizade, salas públicas/particulares, quem pode assistir).
- `internal/signaling`: garante que as mensagens saem no formato que o servidor PeerJS
  aceita (ele derruba quem envia outra coisa).
- `internal/audio`: a decisão de quais apps capturar (ex.: excluir o Discord e o próprio app).
- `internal/stream`: escolha do encoder e parâmetros do FFmpeg (incluindo a regressão do
  quadro-chave da AMD).

### Benchmark de transmissão

Mudanças em `internal/stream`, `internal/audio` ou `internal/ffmpeg` devem vir com o
benchmark antes/depois na descrição do pull request:

```powershell
.\scripts\build.ps1
.\scripts\bench.ps1 -Encoders nvenc,amf -Seconds 20
```

Ele transmite de verdade para um espectador interno e mede fps, bitrate, perda,
regularidade dos quadros e CPU. O resultado varia com o que está na tela: compare as duas
versões com a mesma coisa aberta, intercalando as rodadas.

## Enviando mudanças

1. Crie uma branch a partir da `main` (`feat/nome-curto`, `fix/...`, `docs/...`).
2. Commits pequenos, com mensagens no padrão
   [Conventional Commits](https://www.conventionalcommits.org/pt-br/), em português:
   `feat: volume por pessoa`, `fix: quadro-chave no AMF`, `docs: ...`, `refactor: ...`,
   `test: ...`, `build: ...`, `ci: ...`.
3. Antes de abrir o pull request: `gofmt -l .` (não deve listar nada) e `go test ./...`.
4. Abra o pull request contra a `main` explicando **o que** e **por quê**. O CI compila e
   roda os testes; a `main` só recebe mudanças por pull request.
5. Adicione uma linha em `CHANGELOG.md` (seção "Não lançado") se a mudança afeta quem usa.
   Esse texto aparece no app (Configurações → Novidades): escreva para os amigos.

### Estilo

- Código em Go formatado com `gofmt`; comentários e mensagens em **português**.
- Comente o **porquê** quando não for óbvio — principalmente ajustes de FFmpeg, WebRTC e
  APIs do Windows, que quase sempre têm uma história (ver as notas em ARCHITECTURE.md).
- Mudanças no protocolo entre apps precisam continuar conversando com a versão anterior
  (nem todo mundo atualiza ao mesmo tempo) e devem ser documentadas em
  [docs/PROTOCOL.md](docs/PROTOCOL.md).

## Atualizar o FFmpeg

1. Baixe o build `win64-lgpl` do [BtbN/FFmpeg-Builds](https://github.com/BtbN/FFmpeg-Builds/releases)
   (sempre **lgpl**: a variante GPL mudaria a licença do app).
2. Comprima: `go run ./tools/packffmpeg C:\caminho\ffmpeg.exe internal\ffmpeg\ffmpeg.exe.zst`
3. Teste (encoders, benchmark) e publique o `.zst` num Release de dependência
   (`deps-ffmpeg-<versão>`, marcado como pré-lançamento).
4. Atualize `$Tag` e `$Sha256` em `scripts/fetch-ffmpeg.ps1` e a versão em
   `THIRD_PARTY_NOTICES.md`.

## Lançando uma versão

Versões seguem **0.x.y**: `x` sobe em mudanças relevantes, `y` em ajustes e consertos.

1. No `CHANGELOG.md`, renomeie "Não lançado" para `## [0.x.y] — aaaa-mm-dd`, crie um
   "Não lançado" vazio em cima e atualize os links do fim do arquivo. Faça isso num pull
   request e junte na `main`.
2. Crie e envie a tag na `main`:

   ```powershell
   git tag v0.x.y
   git push origin v0.x.y
   ```

3. O workflow [Release](.github/workflows/release.yml) roda os testes, compila o `.exe`
   com a versão e publica o Release com as notas do changelog. Em até 6 h o app dos
   amigos avisa que há versão nova.
