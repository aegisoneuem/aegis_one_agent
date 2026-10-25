@echo off
setlocal
call "%~dp0..\server-common.cmd"
if errorlevel 1 exit /b 1
echo TEST 17: HKCU is explicitly refused until Run-As
echo EXPECT: FAILED; per-user/HKCU removal requires user-context support
set "AEGIS_DEPLOY_ACTION=uninstall"
set "AEGIS_DEPLOY_TYPE=exe"
set "AEGIS_DEPLOY_NAME=Aegis SoftDist Fixture"
set "AEGIS_DEPLOY_DISPLAY_NAME="
set "AEGIS_DEPLOY_UNINSTALL_KEY=HKCU\AegisSoftDistFixture-v1"
".\mockserver.exe" server
