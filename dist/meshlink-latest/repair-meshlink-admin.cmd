@echo off
setlocal
set "SCRIPT=%~dp0repair-meshlink-admin.ps1"
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%SCRIPT%"
