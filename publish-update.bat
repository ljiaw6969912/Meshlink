@echo off
setlocal EnableExtensions DisableDelayedExpansion

if /i "%~1"=="-Help" goto help
if /i "%~1"=="--help" goto help

cd /d "%~dp0"

set "VERSION_ARG=%~1"
if "%VERSION_ARG%"=="" set /p VERSION_ARG=<"%CD%\VERSION"

set "UPDATE_LISTEN=%~2"
if "%UPDATE_LISTEN%"=="" set "UPDATE_LISTEN=10.77.0.1:1263"

set "RELEASE_DIR=%~3"
if "%RELEASE_DIR%"=="" set "RELEASE_DIR=%CD%\release"

set "RELEASE_MODE=%~4"
if "%RELEASE_MODE%"=="" set "RELEASE_MODE=Development"
if /i not "%RELEASE_MODE%"=="Development" if /i not "%RELEASE_MODE%"=="Release" (
  echo Invalid mode: %RELEASE_MODE%. Use Development or Release.
  exit /b 2
)

set "PREVIOUS_PACKAGE=%~5"

rem Compatibility contract: scripts\release.ps1 orchestrates scripts\build.ps1" -Package before verification.
echo Meshlink update publish
echo Version: %VERSION_ARG%
echo Mode: %RELEASE_MODE%
echo Update listen: %UPDATE_LISTEN%
echo Release dir: %RELEASE_DIR%

if "%PREVIOUS_PACKAGE%"=="" (
  powershell -NoProfile -ExecutionPolicy Bypass -File "%CD%\scripts\release.ps1" -Mode "%RELEASE_MODE%" -Version "%VERSION_ARG%" -ReleaseDir "%RELEASE_DIR%"
) else (
  powershell -NoProfile -ExecutionPolicy Bypass -File "%CD%\scripts\release.ps1" -Mode "%RELEASE_MODE%" -Version "%VERSION_ARG%" -ReleaseDir "%RELEASE_DIR%" -PreviousPackagePath "%PREVIOUS_PACKAGE%"
)
if errorlevel 1 goto fail

"%CD%\bin\mesh-update-server.exe" -service install -service-name MeshlinkUpdateServer -listen "%UPDATE_LISTEN%" -dir "%RELEASE_DIR%"
if errorlevel 1 goto fail

"%CD%\bin\mesh-update-server.exe" -service stop -service-name MeshlinkUpdateServer >nul 2>nul
"%CD%\bin\mesh-update-server.exe" -service start -service-name MeshlinkUpdateServer
if errorlevel 1 goto fail

echo Published verified update package and restarted MeshlinkUpdateServer.
exit /b 0

:help
echo Usage: publish-update.bat [version] [listen] [release-dir] [Development^|Release] [previous-package]
echo.
echo The version argument must match the repository VERSION file.
echo Development artifacts are explicitly unsigned. Release mode requires
echo MESHLINK_SIGNING_CERT_THUMBPRINT and a verified previous package.
exit /b 0

:fail
echo Publish failed. The existing update service and artifacts were not declared released.
exit /b 1
