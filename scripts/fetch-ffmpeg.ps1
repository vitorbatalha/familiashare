# Baixa o FFmpeg embutido (internal/ffmpeg/ffmpeg.exe.zst) e confere o SHA-256.
# O arquivo fica num Release de dependência do repositório (não é versionado no git:
# são 50 MB). Uso: .\scripts\fetch-ffmpeg.ps1
#
# Para trocar a versão do FFmpeg, veja "Atualizar o FFmpeg" no CONTRIBUTING.md.
$ErrorActionPreference = "Stop"

$Tag    = "deps-ffmpeg-n8.1.3"
$Sha256 = "ea76beaedb7c79bf9bc3e15b1dbbbe95582df506c4dc2a05629056b997f4f409"
$Url    = "https://github.com/vitorbatalha/familiashare/releases/download/$Tag/ffmpeg.exe.zst"
$Dest   = Join-Path $PSScriptRoot "..\internal\ffmpeg\ffmpeg.exe.zst"

function Test-Hash($path) {
    (Test-Path $path) -and ((Get-FileHash $path -Algorithm SHA256).Hash.ToLower() -eq $Sha256)
}

if (Test-Hash $Dest) {
    Write-Host "FFmpeg já está aqui e confere ($Tag)."
    exit 0
}

Write-Host "Baixando FFmpeg ($Tag, ~50 MB)..."
$tmp = "$Dest.download"
$ProgressPreference = "SilentlyContinue" # deixa o Invoke-WebRequest muito mais rápido
Invoke-WebRequest $Url -OutFile $tmp -UseBasicParsing
if (-not (Test-Hash $tmp)) {
    Remove-Item $tmp -Force
    throw "SHA-256 não confere: o download veio corrompido ou o arquivo foi alterado."
}
Move-Item $tmp $Dest -Force
Write-Host "OK: $Dest"
