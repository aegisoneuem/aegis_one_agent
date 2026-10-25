@echo off
setlocal
call "%~dp0..\server-common.cmd"
if errorlevel 1 exit /b 1
echo TEST 26: Install fixture and verify version
echo EXPECT: SUCCESS; verified installed (version 1.0)
set "AEGIS_DEPLOY_ACTION=install"
set "AEGIS_DEPLOY_TYPE=cmd"
set "AEGIS_DEPLOY_NAME=Aegis SoftDist Fixture"
set "AEGIS_DEPLOY_DISPLAY_NAME="
set "AEGIS_DEPLOY_FILE=C:\temp\AegisSoftDistTests\install-fixture.cmd"
set "AEGIS_DEPLOY_UNINSTALL_KEY=HKLM64\AegisSoftDistInstall"
set "AEGIS_DEPLOY_VERSION=1.0"
set "AEGIS_DEPLOY_ARGS=1.0"
call "%~dp0..\hash.cmd" "%~dp0..\install-fixture.cmd"
if errorlevel 1 exit /b 1
".\mockserver.exe" server
