@echo off
if "%~1"=="" (
 echo Usage: call C:\temp\AegisSoftDistTests\server-test.cmd 02
 exit /b 1
)
if not exist "%~dp0server\%~1.cmd" (
 echo ERROR: unknown test number. Use two digits: 01 through 34.
 exit /b 1
)
call "%~dp0server\%~1.cmd"
