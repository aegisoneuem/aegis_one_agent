@echo off
setlocal
call "%~dp0..\server-common.cmd"
if errorlevel 1 exit /b 1
echo TEST 34: NSH job propagates native script failure
echo EXPECT: JOB FAILED; native command exit_code=7
echo Use the job run command printed in the guide after selecting the intended endpoint.
".\mockserver.exe" nsh
