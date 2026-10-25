@echo off
setlocal
call "%~dp0..\server-common.cmd"
if errorlevel 1 exit /b 1
echo TEST 23: Installer timeout
echo EXPECT: FAILED; installer timed out after 1s
set "AEGIS_DEPLOY_ACTION=uninstall"
set "AEGIS_DEPLOY_TYPE=cmd"
set "AEGIS_DEPLOY_NAME=Aegis SoftDist Fixture"
set "AEGIS_DEPLOY_DISPLAY_NAME=Aegis SoftDist Fixture"
set "AEGIS_DEPLOY_FILE=C:\temp\AegisSoftDistTests\slow-payload.cmd"
set "AEGIS_DEPLOY_UNINSTALL_KEY=HKLM64\AegisSoftDistFixture-v1"
set "AEGIS_DEPLOY_ARGS="
set "AEGIS_DEPLOY_PARAMS=TAG=signed script"
set "AEGIS_DEPLOY_TIMEOUT=1"
call "%~dp0..\hash.cmd" "%~dp0..\slow-payload.cmd"
if errorlevel 1 exit /b 1
".\mockserver.exe" server
