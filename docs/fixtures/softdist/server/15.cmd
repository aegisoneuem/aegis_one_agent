@echo off
setlocal
call "%~dp0..\server-common.cmd"
if errorlevel 1 exit /b 1
echo TEST 15: Unquoted spaced executable path
echo EXPECT: FAILED; quote paths with spaces
set "AEGIS_DEPLOY_ACTION=uninstall"
set "AEGIS_DEPLOY_TYPE=exe"
set "AEGIS_DEPLOY_NAME=Aegis SoftDist Fixture"
set "AEGIS_DEPLOY_DISPLAY_NAME=Aegis SoftDist Fixture"
set "AEGIS_DEPLOY_UNINSTALL_KEY=HKLM64\AegisSoftDistFixture-v1"
".\mockserver.exe" server
