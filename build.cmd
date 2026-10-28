@echo off
chcp 65001 >nul
setlocal
cd /d "%~dp0"

where go >nul 2>nul
if errorlevel 1 (
  echo [ERROR] go not found in PATH
  echo         Install Go, or add its bin directory to PATH, then reopen the terminal.
  pause
  exit /b 1
)

if not exist "dist" mkdir "dist"

echo Building dist\BlockSync-CLI.exe ...
go build -trimpath -ldflags "-s -w" -o dist\BlockSync-CLI.exe .\cmd\blocksync
if errorlevel 1 goto failed

echo Embedding it into the GUI (single-file mode) ...
if not exist "cmd\blocksync-ui\embedded" mkdir "cmd\blocksync-ui\embedded"
copy /Y "dist\BlockSync-CLI.exe" "cmd\blocksync-ui\embedded\BlockSync-CLI.exe" >nul
if errorlevel 1 goto failed

echo Building dist\BlockSync-GUI.exe ...
go build -trimpath -tags embedcli -ldflags "-H windowsgui -s -w" -o dist\BlockSync-GUI.exe .\cmd\blocksync-ui
if errorlevel 1 goto failed

echo.
echo [OK] dist\BlockSync-CLI.exe and dist\BlockSync-GUI.exe rebuilt
pause
exit /b 0

:failed
echo.
echo [FAILED] build error
pause
exit /b 1
