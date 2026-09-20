@echo off
setlocal
cd /d "%~dp0"

powershell.exe -NoLogo -NoProfile -Command "$p=New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent()); if($p.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)){exit 0}else{exit 1}" >nul 2>&1
if errorlevel 1 (
  echo Requesting administrator privileges...
  powershell.exe -NoLogo -NoProfile -Command "Start-Process -FilePath '%~f0' -Verb RunAs"
  exit /b 0
)

echo Cleaning old outputs and building Meshlink...
powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0scripts\build-clean.ps1" -PrivatePackage
set "BUILD_EXIT=%ERRORLEVEL%"

if not "%BUILD_EXIT%"=="0" (
  echo.
  echo Build failed with exit code %BUILD_EXIT%.
) else (
  echo.
  echo Build completed successfully.
)

echo.
pause
exit /b %BUILD_EXIT%
