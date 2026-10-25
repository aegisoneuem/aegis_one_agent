@echo off
echo SLOW_PAYLOAD_STARTED
"%SystemRoot%\System32\ping.exe" 127.0.0.1 -n 6 >nul
exit /b 0
