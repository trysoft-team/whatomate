# Installs Piper TTS for Windows, matching the Linux/macOS voice
# (Piper 2023.11.14-2 and en_US-lessac-medium), plus ffmpeg from winget.
#
#   powershell -ExecutionPolicy Bypass -File scripts/install-piper.ps1
$ErrorActionPreference = "Stop"

$Version = "2023.11.14-2"
$Model = "en_US-lessac-medium"
$Prefix = Join-Path $env:LOCALAPPDATA "whatomate\piper"
$ModelUrl = "https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US/lessac/medium/${Model}.onnx"
$ZipUrl = "https://github.com/rhasspy/piper/releases/download/${Version}/piper_windows_amd64.zip"

function Have($name) { return [bool](Get-Command $name -ErrorAction SilentlyContinue) }

if (Have winget) {
    Write-Host "Installing ffmpeg with winget"
    winget install --id Gyan.FFmpeg --exact --accept-package-agreements --accept-source-agreements
    if ($LASTEXITCODE -ne 0) { Write-Warning "winget could not install ffmpeg. Install it and put ffmpeg on PATH." }
    Write-Host "Installing espeak-ng with winget (Piper also ships its own espeak data)"
    winget install --id eSpeak-NG.eSpeak-NG --exact --accept-package-agreements --accept-source-agreements
    if ($LASTEXITCODE -ne 0) { Write-Warning "winget could not install espeak-ng." }
} else {
    Write-Warning "winget was not found. Install ffmpeg and opus-tools and put them on PATH."
}

$piper = Join-Path $Prefix "piper.exe"
$modelFile = Join-Path $Prefix "models\${Model}.onnx"
if ((Test-Path $piper) -and (Test-Path $modelFile)) {
    Write-Host "Piper is already installed at $Prefix"
} else {
    $work = Join-Path ([System.IO.Path]::GetTempPath()) ("piper-" + [guid]::NewGuid().ToString("n"))
    New-Item -ItemType Directory -Path $work | Out-Null
    $zip = Join-Path $work "piper.zip"
    Write-Host "Downloading Piper $Version"
    Invoke-WebRequest -Uri $ZipUrl -OutFile $zip
    Expand-Archive -Path $zip -DestinationPath $work -Force
    $extracted = Join-Path $work "piper"
    if (-not (Test-Path (Join-Path $extracted "piper.exe"))) {
        throw "archive did not contain piper\piper.exe"
    }
    New-Item -ItemType Directory -Path (Join-Path $Prefix "models") -Force | Out-Null
    Copy-Item -Path (Join-Path $extracted "*") -Destination $Prefix -Recurse -Force
    Write-Host "Downloading voice $Model"
    Invoke-WebRequest -Uri $ModelUrl -OutFile $modelFile
    Invoke-WebRequest -Uri ($ModelUrl + ".json") -OutFile ($modelFile + ".json")
    Remove-Item -Recurse -Force $work
    Write-Host "Installed Piper at $Prefix"
}

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*${Prefix}*") {
    [Environment]::SetEnvironmentVariable("Path", ($Prefix.TrimEnd('\') + ";" + $userPath), "User")
    Write-Host "Added $Prefix to the user PATH. Open a new terminal before using piper."
}

$opus = @"
Install opusenc and make sure it is on PATH (Whatomate converts Piper's WAV to OGG/Opus):
  https://opus-codec.org/downloads/
"@
if (-not (Have opusenc)) { Write-Warning $opus }

$piperToml = $piper.Replace('\', '/')
$modelToml = $modelFile.Replace('\', '/')
Write-Host @"

Add this to %AppData%\whatomate\config.toml:

[tts]
piper_binary = "$piperToml"
piper_model = "$modelToml"
opusenc_binary = "opusenc"
"@
