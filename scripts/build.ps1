# Gera dist\familiashare.exe (Windows, 64 bits). Requer Go (ver go.mod).
#
#   .\scripts\build.ps1                # build de release (com etiqueta de versão)
#   .\scripts\build.ps1 -Dev           # build rápido, sem etiqueta (não procura atualização)
#
# Versão: cada build de release recebe um identificador = data/hora (ex.: 20261029-141500),
# gravado no programa e numa etiqueta no fim do .exe. O app compara a etiqueta do arquivo
# publicado no Google Drive com a própria para avisar quando há versão nova.
param([switch]$Dev)
# Sem $ErrorActionPreference = "Stop": no PowerShell 5 ele transforma qualquer texto que
# o go escreve no stderr (ex.: "downloading ...") em erro fatal. Cada passo confere
# $LASTEXITCODE.
$Root = Split-Path $PSScriptRoot
Push-Location $Root
try {
    # 1. FFmpeg embutido (baixa se faltar)
    & "$PSScriptRoot\fetch-ffmpeg.ps1"
    if (-not $?) { throw "fetch-ffmpeg falhou" }

    # 2. Ícone e metadados do .exe (assets/icon.png → recurso do Windows)
    Push-Location cmd\familiashare
    go run github.com/tc-hib/go-winres@v0.3.3 simply --icon ..\..\assets\icon.png `
        --product-name familiashare --file-description familiashare --arch amd64
    if ($LASTEXITCODE -ne 0) { throw "go-winres falhou" }
    Pop-Location

    # 3. Compilação
    New-Item -ItemType Directory -Force dist -ErrorAction Stop | Out-Null
    $out = Join-Path $Root "dist\familiashare.exe"
    $ldflags = "-s -w -H windowsgui"
    if (-not $Dev) {
        $version = Get-Date -Format "yyyyMMdd-HHmmss"
        $ldflags += " -X github.com/vitorbatalha/familiashare/internal/update.BuildID=$version"
    }
    Write-Host "Compilando$(if ($Dev) { ' (dev)' } else { " versão $version" })..."
    go build -trimpath -ldflags $ldflags -o $out ./cmd/familiashare
    if ($LASTEXITCODE -ne 0) { throw "go build falhou" }

    # 4. Etiqueta de versão no fim do arquivo (o Windows ignora dados após o fim do executável)
    if (-not $Dev) {
        $tag = [Text.Encoding]::ASCII.GetBytes("`nFAMILIASHARE-BUILD:$version`n")
        $fs = [IO.File]::Open($out, 'Append'); $fs.Write($tag, 0, $tag.Length); $fs.Close()
    }
    Write-Host ("OK: dist\familiashare.exe ({0:N1} MB)" -f ((Get-Item $out).Length / 1MB))
} finally { Pop-Location }
