@echo off
setlocal
cd /d "%~dp0"
set "MESHLINK_BUILD_SCRIPT=%~dp0scripts\build-all.ps1"

echo Building and packaging Windows and Linux Meshlink.
echo Existing release configuration and identity will be preserved.
echo Build log: %~dp0.cache\build-all.log

powershell.exe -NoLogo -NoProfile -Command "$p=New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent()); if($p.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)){exit 0}else{exit 1}" >nul 2>&1
if errorlevel 1 (
  echo Requesting administrator privileges...
  powershell.exe -NoLogo -NoProfile -Command "$ErrorActionPreference='Stop'; try { $p=Start-Process -FilePath 'powershell.exe' -ArgumentList @('-NoLogo','-NoProfile','-ExecutionPolicy','Bypass','-File',('\"' + $env:MESHLINK_BUILD_SCRIPT + '\"')) -Verb RunAs -WindowStyle Hidden -PassThru; $p.WaitForExit(); exit $p.ExitCode } catch { Write-Error $_; exit 1 }"
  goto report
)

powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%MESHLINK_BUILD_SCRIPT%"
:report
set "BUILD_EXIT=%ERRORLEVEL%"

if not "%BUILD_EXIT%"=="0" (
  echo.
  echo Build failed with exit code %BUILD_EXIT%.
) else (
  echo.
  echo Build completed successfully.
)

echo.
echo Details: %~dp0.cache\build-all.log
pause
exit /b %BUILD_EXIT%
