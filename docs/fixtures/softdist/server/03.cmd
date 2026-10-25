@echo off
setlocal
call "%~dp0..\server-common.cmd"
if errorlevel 1 exit /b 1
echo TEST 03: Already absent requested version
echo EXPECT: NOT_APPLICABLE; not installed
set "AEGIS_DEPLOY_ACTION=uninstall"
set "AEGIS_DEPLOY_TYPE=exe"
set "AEGIS_DEPLOY_NAME=Aegis SoftDist Fixture"
set "AEGIS_DEPLOY_DISPLAY_NAME=Aegis SoftDist Fixture"
set "AEGIS_DEPLOY_VERSION=1.0"
".\mockserver.exe" server
