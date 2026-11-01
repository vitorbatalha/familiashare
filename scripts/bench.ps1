# Benchmark de transmissão: roda o familiashare.exe em modo benchmark com vários
# encoders e imprime uma linha por rodada. Ver cmd/familiashare/bench.go.
#
#   .\scripts\bench.ps1                          # usa dist\familiashare.exe, 20 s por rodada
#   .\scripts\bench.ps1 -Exe outro.exe -Seconds 30 -Encoders nvenc,amf
#
# Atenção: o benchmark captura a sua tela de verdade (nada sai do PC). O bitrate
# varia com o que está na tela; para comparar duas versões, rode as duas com a
# mesma coisa aberta (ou com a tela parada).
param(
    [string]$Exe = (Join-Path (Split-Path $PSScriptRoot) "dist\familiashare.exe"),
    [int]$Seconds = 20,
    [string[]]$Encoders = @("nvenc", "amf", "qsv"),
    [string[]]$Codecs = @("h264"),
    [int]$Mbps = 12
)
$ErrorActionPreference = "Stop"
$data = Join-Path $env:TEMP "familiashare-bench"
New-Item -ItemType Directory -Force $data | Out-Null
$env:FAMILIASHARE_DATA = $data   # não mexe nos seus dados reais

foreach ($codec in $Codecs) {
    foreach ($enc in $Encoders) {
        $env:FAMILIASHARE_BENCH = "$Seconds"
        $env:FAMILIASHARE_BENCH_ENCODER = $enc
        $env:FAMILIASHARE_BENCH_CODEC = $codec
        $env:FAMILIASHARE_BENCH_MBPS = "$Mbps"
        $out = Join-Path $data "bench-out.txt"
        Start-Process $Exe -RedirectStandardOutput $out -WindowStyle Hidden -Wait | Out-Null
        $j = Get-Content $out -Raw | ConvertFrom-Json
        if ($j.err) { "{0,-6} {1,-5} | erro: {2}" -f $enc, $codec, $j.err; continue }
        "{0,-6} {1,-5} {2,3}M | {3} | envio {4,5:N1} fps | recebe {5,5:N1} fps {6,5:N1} Mbps | perda {7,5:N2}% | intervalo p50 {8,5:N1} p95 {9,5:N1} máx {10,6:N1} ms | CPU app {11,5:N1}% ffmpeg {12,5:N1}%" -f `
            $enc, $codec, $Mbps, $j.pipeline, $j.send_fps, $j.recv_fps, $j.recv_mbps, $j.loss_pct, $j.gap_p50_ms, $j.gap_p95_ms, $j.gap_max_ms, $j.cpu_app_pct, $j.cpu_ffmpeg_pct
    }
}
