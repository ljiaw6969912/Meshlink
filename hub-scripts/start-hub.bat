@echo off
setlocal

set "HUB=%~dp0mesh-cloudhub.exe"
if not exist "%HUB%" (
  echo mesh-cloudhub.exe is missing.
  echo Copy this script into the directory containing mesh-cloudhub.exe.
  echo.
  pause
  exit /b 1
)

echo Development only: this Hub uses in-memory storage and has no production TLS or persistence.
echo Control API: http://0.0.0.0:18080
echo Relay TCP:   0.0.0.0:18082
echo Press Ctrl+C to stop.
echo.

"%HUB%" -listen 0.0.0.0:18080 -relay-listen 0.0.0.0:18082
set "HUB_EXIT=%ERRORLEVEL%"

if not "%HUB_EXIT%"=="0" (
  echo.
  echo Official Hub stopped with exit code %HUB_EXIT%.
  echo Check whether ports 18080 or 18082 are already in use.
  echo.
  pause
)

exit /b %HUB_EXIT%
