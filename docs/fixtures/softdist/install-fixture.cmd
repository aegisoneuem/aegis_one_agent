@echo off
setlocal
if not exist C:\temp\AegisSoftDistTests\results mkdir C:\temp\AegisSoftDistTests\results
reg add "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistInstall" /v DisplayName /t REG_SZ /d "Aegis SoftDist Install Fixture" /f /reg:64
if errorlevel 1 exit /b 21
if /i "%~1"=="unknown" goto unknown
reg add "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistInstall" /v DisplayVersion /t REG_SZ /d "%~1" /f /reg:64
if errorlevel 1 exit /b 22
goto report
:unknown
reg delete "HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\AegisSoftDistInstall" /v DisplayVersion /f /reg:64 >nul 2>&1
:report
echo INSTALL_FIXTURE version=%~1 host=%AEGIS_PARAM_HOSTNAME%>"C:\temp\AegisSoftDistTests\results\install.txt"
type "C:\temp\AegisSoftDistTests\results\install.txt"
exit /b 0
