@echo off
setlocal
call "%~dp0..\server-common.cmd"
if errorlevel 1 exit /b 1
echo TEST 33: NSH Software Distribution job, parameters and fleet loop
echo EXPECT: JOB SUCCESS; per-target package/tag/hostname output
echo Use the job run command printed in the guide after selecting the intended endpoint.
".\mockserver.exe" nsh
