# Gera dist\familiashare.exe (Windows, 64 bits). Requer Go (ver go.mod).
#
#   .\scripts\build.ps1                    # build de desenvolvimento (não procura atualização)
#   .\scripts\build.ps1 -Version 0.2.0     # build de release
#
# O build de release grava a versão no programa (o app compara com o último release do
# GitHub) e anexa no fim do .exe a etiqueta "FAMILIASHARE-BUILD:aaaammdd-hhmmss", que é
# como as versões até a 0.1.2 reconheciam uma atualização no Google Drive.
param([string]$Version)
# Sem $ErrorActionPreference = "Stop": no PowerShell 5 ele transforma qualquer texto que
# o go escreve no stderr (ex.: "downloading ...") em erro fatal. Cada passo confere
# $LASTEXITCODE.
if ($Version -and $Version -notmatch '^\d+\.\d+\.\d+$') { throw "Versão deve ser 0.x.y (veio '$Version')" }
$Root = Split-Path $PSScriptRoot
Push-Location $Root
try {
    # 1. FFmpeg embutido (baixa se faltar)
    & "$PSScriptRoot\fetch-ffmpeg.ps1"
    if (-not $?) { throw "fetch-ffmpeg falhou" }

    # 2. Ícone e metadados do .exe (assets/icon.png → recurso do Windows)
    $fileVer = if ($Version) { "$Version.0" } else { "0.0.0.0" }
    Push-Location cmd\familiashare
    go run github.com/tc-hib/go-winres@v0.3.3 simply --icon ..\..\assets\icon.png `
        --product-name familiashare --file-description familiashare --arch amd64 `
        --file-version $fileVer --product-version $fileVer
    if ($LASTEXITCODE -ne 0) { throw "go-winres falhou" }
    Pop-Location

    # 3. Compilação
    New-Item -ItemType Directory -Force dist -ErrorAction Stop | Out-Null
    $out = Join-Path $Root "dist\familiashare.exe"
    $ldflags = "-s -w -H windowsgui"
    if ($Version) { $ldflags += " -X github.com/vitorbatalha/familiashare/internal/update.Version=$Version" }
    Write-Host "Compilando $(if ($Version) { "versão $Version" } else { '(desenvolvimento)' })..."
    go build -trimpath -ldflags $ldflags -o $out ./cmd/familiashare
    if ($LASTEXITCODE -ne 0) { throw "go build falhou" }

    # 4. Etiqueta para as versões antigas (o Windows ignora dados após o fim do executável)
    if ($Version) {
        $stamp = Get-Date -Format "yyyyMMdd-HHmmss"
        $tag = [Text.Encoding]::ASCII.GetBytes("`nFAMILIASHARE-BUILD:$stamp`n")
        $fs = [IO.File]::Open($out, 'Append'); $fs.Write($tag, 0, $tag.Length); $fs.Close()
    }
    Write-Host ("OK: dist\familiashare.exe ({0:N1} MB)" -f ((Get-Item $out).Length / 1MB))
} finally { Pop-Location }
