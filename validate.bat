@echo off
setlocal
cd /d "%~dp0"
if exist "%LOCALAPPDATA%\Programs\Go\bin\go.exe" set "PATH=%LOCALAPPDATA%\Programs\Go\bin;%PATH%"

echo [1/5] Checking Go formatting (gofmt)...
for /f "delims=" %%F in ('gofmt -l cmd pkg tests') do (
  echo ERROR: Unformatted Go file: %%F 1>&2
  exit /b 1
)

echo [2/5] Running Go static analysis with CGO_ENABLED=0 (go vet ./...)...
set CGO_ENABLED=0
go vet ./...
if errorlevel 1 exit /b 1

echo [3/5] Running portable test suite with CGO_ENABLED=0 (go test -count=1 ./...)...
set CGO_ENABLED=0
go test -count=1 ./...
if errorlevel 1 exit /b 1

echo [4/5] Running concurrency race detector suite (go test -race -count=1 ./...)...
where gcc >nul 2>nul
if %errorlevel%==0 (
  set CGO_ENABLED=1
  go test -race -count=1 ./...
  if errorlevel 1 exit /b 1
) else (
  echo INFO: C compiler not found in PATH; skipping -race pass on this workstation.
)
set CGO_ENABLED=0

echo [5/5] Building cross-platform Worker release binaries (build.bat)...
call .\build.bat
if errorlevel 1 exit /b 1

echo ============================================================================
echo FULL WORKER VALIDATION PASSED: Formatting, Vet, Arch/Unit/Int Tests, Build!
echo ============================================================================
