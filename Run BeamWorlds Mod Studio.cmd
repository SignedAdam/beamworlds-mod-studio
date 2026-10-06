@echo off
setlocal
set "APP=%~dp0studio\BeamNGModStudio\bin\beamngmodstudio.exe"
if not exist "%APP%" (
  echo BeamWorlds Mod Studio executable was not found.
  echo Expected: %APP%
  pause
  exit /b 1
)
start "BeamWorlds Mod Studio" /D "%~dp0" "%APP%"
