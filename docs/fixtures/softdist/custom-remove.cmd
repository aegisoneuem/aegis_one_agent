@echo off
setlocal
if not exist C:\temp\AegisSoftDistTests\results mkdir C:\temp\AegisSoftDistTests\results
(
echo CUSTOM_REMOVE
echo tag=%~1
echo host-argument=%~2
echo tag-environment=%AEGIS_PARAM_TAG%
echo host-environment=%AEGIS_PARAM_HOSTNAME%
) > "C:\temp\AegisSoftDistTests\results\custom.txt"
type "C:\temp\AegisSoftDistTests\results\custom.txt"
reg delete "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v1" /f /reg:64
if errorlevel 1 exit /b 20
if "%~3"=="" exit /b 0
exit /b %~3
