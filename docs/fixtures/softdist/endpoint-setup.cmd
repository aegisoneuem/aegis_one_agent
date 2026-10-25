@echo off
setlocal
if not exist "%~dp0endpoint\%~1.reg" (
 echo Usage: call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 02
 exit /b 1
)
if /i not "%~dp0"=="C:\temp\AegisSoftDistTests\" (
 echo ERROR: copy this test-pack folder to C:\temp\AegisSoftDistTests first.
 exit /b 1
)
if not exist C:\temp\AegisSoftDistTests\results mkdir C:\temp\AegisSoftDistTests\results
if not exist "C:\temp\AegisSoftDistTests\Payload With Spaces" mkdir "C:\temp\AegisSoftDistTests\Payload With Spaces"
copy /y "%~dp0custom-remove.cmd" "C:\temp\AegisSoftDistTests\Payload With Spaces\custom-remove.cmd" >nul
for %%F in (custom.txt install.txt nsh.txt) do if exist "%~dp0results\%%F" del /q "%~dp0results\%%F"
reg import "%~dp0endpoint\%~1.reg"
if errorlevel 1 (
 echo SETUP FAILED. Use Administrator CMD on the endpoint VM.
 exit /b 1
)
echo Setup %~1 complete on %COMPUTERNAME%. Now run server-test.cmd %~1 on the mockserver machine.
exit /b 0
