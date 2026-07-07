@echo off
setlocal EnableExtensions

cd /d "%~dp0"

set "VERSION_ARG=%~1"
if "%VERSION_ARG%"=="" (
  set /p VERSION_ARG=Version to publish, blank keeps current VERSION:
)
if not "%VERSION_ARG%"=="" (
  > VERSION echo %VERSION_ARG%
)

set "UPDATE_LISTEN=%~2"
if "%UPDATE_LISTEN%"=="" set "UPDATE_LISTEN=10.77.0.1:1263"

set "RELEASE_DIR=%~3"
if "%RELEASE_DIR%"=="" set "RELEASE_DIR=%CD%\release"

echo.
echo Meshlink update publish
echo Version file: %CD%\VERSION
echo Update listen: %UPDATE_LISTEN%
echo Release dir: %RELEASE_DIR%
echo.

powershell -NoProfile -ExecutionPolicy Bypass -File "%CD%\scripts\build.ps1" -Package
if errorlevel 1 goto fail

"%CD%\bin\mesh-update-server.exe" -service install -service-name MeshlinkUpdateServer -listen "%UPDATE_LISTEN%" -dir "%RELEASE_DIR%"
if errorlevel 1 goto fail

"%CD%\bin\mesh-update-server.exe" -service stop -service-name MeshlinkUpdateServer >nul 2>nul
"%CD%\bin\mesh-update-server.exe" -service start -service-name MeshlinkUpdateServer
if errorlevel 1 goto fail

echo.
echo Published update package and restarted MeshlinkUpdateServer.
echo Clients can check updates from Help - About / Check Updates.
pause
exit /b 0

:fail
echo.
echo Publish failed. Check the command output above.
pause
exit /b 1
