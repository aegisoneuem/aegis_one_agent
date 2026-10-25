@echo off
setlocal
call "%~dp0..\server-common.cmd"
if errorlevel 1 exit /b 1
echo TEST 18: Custom command overrides default registry uninstaller
echo EXPECT: SUCCESS; verified removed; exit_code=0
set "AEGIS_DEPLOY_ACTION=uninstall"
set "AEGIS_DEPLOY_TYPE=command"
set "AEGIS_DEPLOY_NAME=Aegis SoftDist Fixture"
set "AEGIS_DEPLOY_DISPLAY_NAME=Aegis SoftDist Fixture"
set "AEGIS_DEPLOY_FILE=%SystemRoot%\System32\cmd.exe"
set "AEGIS_DEPLOY_UNINSTALL_KEY=HKLM64\AegisSoftDistFixture-v1"
set "AEGIS_DEPLOY_ARGS=/d /c C:\temp\AegisSoftDistTests\custom-remove.cmd "${TAG}" "${hostname}""
set "AEGIS_DEPLOY_PARAMS=TAG=custom command"
".\mockserver.exe" server
