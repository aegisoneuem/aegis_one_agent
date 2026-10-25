@echo off
if not exist "%~1" (
 echo ERROR: hash source file missing: %~1
 exit /b 1
)
set "AEGIS_DEPLOY_HASH="
for /f "skip=1 tokens=*" %%H in ('certutil -hashfile "%~1" SHA256 2^>nul') do if not defined AEGIS_DEPLOY_HASH set "AEGIS_DEPLOY_HASH=%%H"
set "AEGIS_DEPLOY_HASH=%AEGIS_DEPLOY_HASH: =%"
if not defined AEGIS_DEPLOY_HASH exit /b 1
set "AEGIS_DEPLOY_HASH_ALGO=sha256"
exit /b 0
