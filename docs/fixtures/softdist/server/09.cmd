@echo off
setlocal
call "%~dp0..\server-common.cmd"
if errorlevel 1 exit /b 1
echo TEST 09: Key and display name disagree
echo EXPECT: NOT_APPLICABLE; not installed
set "AEGIS_DEPLOY_ACTION=uninstall"
set "AEGIS_DEPLOY_TYPE=exe"
set "AEGIS_DEPLOY_NAME=Aegis SoftDist Fixture"
set "AEGIS_DEPLOY_DISPLAY_NAME=A completely different fixture"
set "AEGIS_DEPLOY_UNINSTALL_KEY=HKLM64\AegisSoftDistFixture-v1"
".\mockserver.exe" server
