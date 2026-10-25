@echo off
setlocal
call "%~dp0..\server-common.cmd"
if errorlevel 1 exit /b 1
echo TEST 20: Tampered or wrong-hash file is refused
echo EXPECT: FAILED; integrity check failed (hash mismatch)
set "AEGIS_DEPLOY_ACTION=uninstall"
set "AEGIS_DEPLOY_TYPE=cmd"
set "AEGIS_DEPLOY_NAME=Aegis SoftDist Fixture"
set "AEGIS_DEPLOY_DISPLAY_NAME=Aegis SoftDist Fixture"
set "AEGIS_DEPLOY_FILE=C:\temp\AegisSoftDistTests\custom-remove.cmd"
set "AEGIS_DEPLOY_UNINSTALL_KEY=HKLM64\AegisSoftDistFixture-v1"
set "AEGIS_DEPLOY_ARGS="${TAG}" "${hostname}""
set "AEGIS_DEPLOY_PARAMS=TAG=signed script"
set "AEGIS_DEPLOY_HASH=0000000000000000000000000000000000000000000000000000000000000000"
".\mockserver.exe" server
