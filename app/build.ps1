# Gera familiashare.exe (Windows, 64 bits) na pasta acima desta.
# Requer Go 1.25+. Uso:
#   .\build.ps1                                  (usa o ffmpeg.exe.zst que já está aqui)
#   .\build.ps1 -FFmpeg C:\caminho\ffmpeg.exe    (troca o FFmpeg embutido; build "lgpl" do BtbN)
#
# Versão: cada build recebe um identificador = data/hora (ex.: 20261029-141500), gravado no
# programa e numa etiqueta no fim do .exe. O app compara a etiqueta do arquivo publicado no
# Google Drive com a própria para avisar quando há versão nova — basta subir o .exe novo
# no MESMO arquivo/link do Drive.
#
# Ícone: vem de icon.png, já convertido em rsrc_windows_amd64.syso. Se trocar a imagem, rode:
#   go run github.com/tc-hib/go-winres@v0.3.3 simply --icon icon.png --product-name familiashare --file-description familiashare --arch amd64
param([string]$FFmpeg)
$ErrorActionPreference = "Stop"
Push-Location $PSScriptRoot
try {
    if ($FFmpeg -and (-not (Test-Path ffmpeg.exe.zst) -or (Get-Item $FFmpeg).LastWriteTime -gt (Get-Item ffmpeg.exe.zst).LastWriteTime)) {
        Write-Host "Comprimindo FFmpeg..."
        go run ./cmd/pack $FFmpeg ffmpeg.exe.zst
    }
    if (-not (Test-Path ffmpeg.exe.zst)) { throw "falta ffmpeg.exe.zst: rode com -FFmpeg C:\caminho\ffmpeg.exe" }

    $version = Get-Date -Format "yyyyMMdd-HHmmss"
    $out = Join-Path (Split-Path $PSScriptRoot) "familiashare.exe"
    Write-Host "Compilando versão $version..."
    go build -trimpath -ldflags "-s -w -H windowsgui -X main.buildID=$version" -o $out .
    if ($LASTEXITCODE -ne 0) { throw "go build falhou" }

    # etiqueta de versão no fim do arquivo (o Windows ignora dados após o fim do executável)
    $tag = [Text.Encoding]::ASCII.GetBytes("`nFAMILIASHARE-BUILD:$version`n")
    $fs = [IO.File]::Open($out, 'Append'); $fs.Write($tag, 0, $tag.Length); $fs.Close()

    Write-Host ("OK: familiashare.exe ({0:N1} MB) — versão {1}" -f ((Get-Item $out).Length / 1MB), $version)
    Write-Host "Suba este arquivo substituindo o do Drive (mesmo link) para os amigos receberem o aviso."
} finally { Pop-Location }
