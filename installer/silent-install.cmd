@echo off
rem ==========================================================================
rem  AegisOne Client — silent install driven by a config file.
rem
rem  Usage:
rem    silent-install.cmd                      (uses aegisone.cfg + AegisOneClient.msi next to this script)
rem    silent-install.cmd  my.cfg              (custom config file)
rem    silent-install.cmd  my.cfg  path\to.msi (custom config + MSI)
rem
rem  The config file is KEY=VALUE lines (see aegisone.cfg). Each line becomes an
rem  msiexec property. This is the "response file" equivalent of passing the same
rem  properties on the command line.
rem ==========================================================================
setlocal enabledelayedexpansion

set "SCRIPTDIR=%~dp0"
set "CFG=%~1"
if "%CFG%"=="" set "CFG=%SCRIPTDIR%aegisone.cfg"
set "MSI=%~2"
if "%MSI%"=="" set "MSI=%SCRIPTDIR%AegisOneClient.msi"

if not exist "%CFG%" ( echo [ERROR] config file not found: %CFG% & exit /b 1 )
if not exist "%MSI%" ( echo [ERROR] MSI not found: %MSI% & exit /b 1 )

set "PROPS="
for /f "usebackq eol=# tokens=1,* delims==" %%A in ("%CFG%") do (
  set "K=%%A"
  set "V=%%B"
  if not "!K!"=="" set "PROPS=!PROPS! !K!=^"!V!^""
)

echo Installing "%MSI%"
echo Properties:!PROPS!
msiexec /i "%MSI%" /qn!PROPS! /l*v "%SCRIPTDIR%silent-install.log"
set "RC=%ERRORLEVEL%"
if "%RC%"=="0" ( echo [OK] install complete ) else ( echo [FAILED] exit code %RC% ^(see silent-install.log^) )
endlocal & exit /b %RC%
