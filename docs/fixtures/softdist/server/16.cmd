@echo off
setlocal
call "%~dp0..\server-common.cmd"
if errorlevel 1 exit /b 1
echo TEST 16: Malformed MSI ProductCode
echo EXPECT: FAILED; invalid MSI ProductCode
set "AEGIS_DEPLOY_ACTION=uninstall"
set "AEGIS_DEPLOY_TYPE=exe"
set "AEGIS_DEPLOY_NAME=Aegis SoftDist Fixture"
set "AEGIS_DEPLOY_DISPLAY_NAME="
set "AEGIS_DEPLOY_PRODUCT_CODE=not-a-guid"
".\mockserver.exe" server
