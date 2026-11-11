@echo off
setlocal
cd /d "%~dp0"
if exist "%LOCALAPPDATA%\Programs\Go\bin\go.exe" set "PATH=%LOCALAPPDATA%\Programs\Go\bin;%PATH%"
if not exist dist mkdir dist

echo Building Worker Desktop and CLI (windows/amd64)...
set GOOS=windows
set GOARCH=amd64
set CGO_ENABLED=0
go build -trimpath -ldflags="-s -w -H=windowsgui" -o dist\gpu-worker-windows-amd64.exe .\cmd\worker
if errorlevel 1 exit /b 1

echo Building Worker Desktop and CLI (linux/amd64)...
set GOOS=linux
set GOARCH=amd64
set CGO_ENABLED=0
go build -trimpath -ldflags="-s -w" -o dist\gpu-worker-linux-amd64 .\cmd\worker
if errorlevel 1 exit /b 1

echo Generating LF-normalized SHA256SUMS manifest...
powershell -NoProfile -ExecutionPolicy Bypass -Command "$files = @('gpu-worker-windows-amd64.exe','gpu-worker-linux-amd64'); $lines = foreach ($f in $files) { $h = (Get-FileHash -Algorithm SHA256 (Join-Path 'dist' $f)).Hash.ToLowerInvariant(); $h + '  ' + $f }; $out = ($lines -join [char]10) + [char]10; [System.IO.File]::WriteAllText((Join-Path (Get-Location) 'dist\SHA256SUMS'), $out, [System.Text.UTF8Encoding]::new($false))"
if errorlevel 1 exit /b 1

echo Build succeeded: binaries and SHA256SUMS written to .\dist
