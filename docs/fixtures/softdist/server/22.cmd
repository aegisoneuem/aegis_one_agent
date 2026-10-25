@echo off
setlocal
call "%~dp0..\server-common.cmd"
if errorlevel 1 exit /b 1
echo TEST 22: Server-delivered custom script with detection IDs
echo EXPECT: SUCCESS; verified removed; FetchContent kind=package
set "AEGIS_DEPLOY_ACTION=uninstall"
set "AEGIS_DEPLOY_TYPE=cmd"
set "AEGIS_DEPLOY_NAME=Aegis SoftDist Fixture"
set "AEGIS_DEPLOY_DISPLAY_NAME=Aegis SoftDist Fixture"
set "AEGIS_DEPLOY_FILE="
set "AEGIS_DEPLOY_UNINSTALL_KEY=HKLM64\AegisSoftDistFixture-v1"
set "AEGIS_DEPLOY_ARGS="${TAG}" "${hostname}""
set "AEGIS_DEPLOY_PARAMS=TAG=signed script"
set "AEGIS_DEPLOY_CONTENT_ID=softdist-test-22"
call "%~dp0..\hash.cmd" "%~dp0..\custom-remove.cmd"
if errorlevel 1 exit /b 1
set "AEGIS_SERVE_SOFTWARE=%~dp0..\custom-remove.cmd"
".\mockserver.exe" server
