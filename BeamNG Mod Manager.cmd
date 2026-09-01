@echo off
setlocal
cd /d "%~dp0"
where node.exe >nul 2>nul
if errorlevel 1 (
  echo Node.js is required but was not found.
  pause
  exit /b 1
)
node server.js
if errorlevel 1 pause
