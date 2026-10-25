@echo off
setlocal
call "%~dp0..\server-common.cmd"
if errorlevel 1 exit /b 1
echo TEST 29: Missing pinned hash is refused
echo EXPECT: FAILED; expected_hash (sha256) is required
set "AEGIS_DEPLOY_ACTION=uninstall"
set "AEGIS_DEPLOY_TYPE=cmd"
set "AEGIS_DEPLOY_NAME=Aegis SoftDist Fixture"
set "AEGIS_DEPLOY_DISPLAY_NAME=Aegis SoftDist Fixture"
set "AEGIS_DEPLOY_FILE=C:\temp\AegisSoftDistTests\custom-remove.cmd"
set "AEGIS_DEPLOY_UNINSTALL_KEY=HKLM64\AegisSoftDistFixture-v1"
set "AEGIS_DEPLOY_ARGS="${TAG}" "${hostname}""
set "AEGIS_DEPLOY_PARAMS=TAG=signed script"
set "AEGIS_DEPLOY_AUTO_HASH=0"
".\mockserver.exe" server
