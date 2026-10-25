@echo off
setlocal
set "failures=0"
if "%~1"=="" goto usage
goto case%~1
:case01
echo Checking test 01 on %COMPUTERNAME%
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v1" 64
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v2" 64
goto summary
:case02
echo Checking test 02 on %COMPUTERNAME%
call :absent "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v1" 64
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v2" 64
goto summary
:case03
echo Checking test 03 on %COMPUTERNAME%
call :absent "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v1" 64
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v2" 64
goto summary
:case04
echo Checking test 04 on %COMPUTERNAME%
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v1" 64
call :absent "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v2" 64
goto summary
:case05
echo Checking test 05 on %COMPUTERNAME%
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-view" 32
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-view" 64
goto summary
:case06
echo Checking test 06 on %COMPUTERNAME%
call :absent "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-view" 32
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-view" 64
goto summary
:case07
echo Checking test 07 on %COMPUTERNAME%
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-view" 32
call :absent "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-view" 64
goto summary
:case08
echo Checking test 08 on %COMPUTERNAME%
call :absent "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v1" 64
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-tool" 64
goto summary
:case09
echo Checking test 09 on %COMPUTERNAME%
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v1" 64
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v2" 64
goto summary
:case10
echo Checking test 10 on %COMPUTERNAME%
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v1" 64
goto summary
:case11
echo Checking test 11 on %COMPUTERNAME%
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v1" 64
call :no-marker "C:\temp\AegisSoftDistTests\results\custom.txt"
goto summary
:case12
echo Checking test 12 on %COMPUTERNAME%
call :absent "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v1" 64
call :marker "C:\temp\AegisSoftDistTests\results\custom.txt"
goto summary
:case13
echo Checking test 13 on %COMPUTERNAME%
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v1" 64
goto summary
:case14
echo Checking test 14 on %COMPUTERNAME%
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v1" 64
goto summary
:case15
echo Checking test 15 on %COMPUTERNAME%
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v1" 64
goto summary
:case16
echo Checking test 16 on %COMPUTERNAME%
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v1" 64
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v2" 64
goto summary
:case17
echo Checking test 17 on %COMPUTERNAME%
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v1" 64
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v2" 64
goto summary
:case18
echo Checking test 18 on %COMPUTERNAME%
call :absent "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v1" 64
call :marker "C:\temp\AegisSoftDistTests\results\custom.txt"
goto summary
:case19
echo Checking test 19 on %COMPUTERNAME%
call :absent "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v1" 64
call :marker "C:\temp\AegisSoftDistTests\results\custom.txt"
goto summary
:case20
echo Checking test 20 on %COMPUTERNAME%
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v1" 64
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v2" 64
call :no-marker "C:\temp\AegisSoftDistTests\results\custom.txt"
goto summary
:case21
echo Checking test 21 on %COMPUTERNAME%
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v1" 64
goto summary
:case22
echo Checking test 22 on %COMPUTERNAME%
call :absent "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v1" 64
call :marker "C:\temp\AegisSoftDistTests\results\custom.txt"
goto summary
:case23
echo Checking test 23 on %COMPUTERNAME%
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v1" 64
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v2" 64
goto summary
:case24
echo Checking test 24 on %COMPUTERNAME%
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v1" 64
goto summary
:case25
echo Checking test 25 on %COMPUTERNAME%
call :absent "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v1" 64
call :marker "C:\temp\AegisSoftDistTests\results\custom.txt"
goto summary
:case26
echo Checking test 26 on %COMPUTERNAME%
call :version "1.0"
call :marker "C:\temp\AegisSoftDistTests\results\install.txt"
goto summary
:case27
echo Checking test 27 on %COMPUTERNAME%
call :version "2.0"
call :marker "C:\temp\AegisSoftDistTests\results\install.txt"
goto summary
:case28
echo Checking test 28 on %COMPUTERNAME%
call :version "2.0"
call :no-marker "C:\temp\AegisSoftDistTests\results\install.txt"
goto summary
:case29
echo Checking test 29 on %COMPUTERNAME%
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v1" 64
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v2" 64
call :no-marker "C:\temp\AegisSoftDistTests\results\custom.txt"
goto summary
:case30
echo Checking test 30 on %COMPUTERNAME%
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v1" 64
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v2" 64
call :no-marker "C:\temp\AegisSoftDistTests\results\custom.txt"
goto summary
:case31
echo Checking test 31 on %COMPUTERNAME%
call :absent "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistFixture-v1" 64
call :marker "C:\temp\AegisSoftDistTests\results\custom.txt"
goto summary
:case32
echo Checking test 32 on %COMPUTERNAME%
call :present "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistInstall" 64
reg query "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistInstall" /v DisplayVersion /reg:64 >nul 2>&1
if not errorlevel 1 call :failed "DisplayVersion should be absent"
call :marker "C:\temp\AegisSoftDistTests\results\install.txt"
goto summary
:case33
echo Checking test 33 on %COMPUTERNAME%
call :marker "C:\temp\AegisSoftDistTests\results\nsh.txt"
goto summary
:case34
echo Checking test 34 on %COMPUTERNAME%
goto summary

:summary
if not "%failures%"=="0" (
 echo ENDPOINT CHECK FAILED: %failures% mismatch(es).
 exit /b 1
)
echo ENDPOINT CHECK PASS. Also compare the intended host's terminal ActionStatus with the guide.
exit /b 0
:present
reg query "%~1" /reg:%~2 >nul 2>&1
if errorlevel 1 call :failed "Expected present: %~1 view %~2"
exit /b 0
:absent
reg query "%~1" /reg:%~2 >nul 2>&1
if not errorlevel 1 call :failed "Expected absent: %~1 view %~2"
exit /b 0
:marker
if not exist "%~1" call :failed "Missing marker: %~1"
if exist "%~1" type "%~1"
exit /b 0
:no-marker
if exist "%~1" call :failed "Payload should not have run: %~1"
exit /b 0
:version
reg query "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistInstall" /v DisplayVersion /reg:64 | findstr /r /c:"REG_SZ *%~1$" >nul
if errorlevel 1 call :failed "Wrong DisplayVersion; expected %~1"
exit /b 0
:failed
echo FAIL: %~1
set /a failures+=1 >nul
exit /b 0
:usage
echo Usage: call C:\temp\AegisSoftDistTests\endpoint-check.cmd 02
exit /b 1
