# Copy this whole folder

Copy the entire folder to `C:\temp\AegisSoftDistTests` on both the endpoint VM and the mockserver machine.

For test 02:

1. Endpoint Administrator CMD: `call C:\temp\AegisSoftDistTests\endpoint-setup.cmd 02`
2. Mockserver CMD (in its EXE/certs folder): `call C:\temp\AegisSoftDistTests\server-test.cmd 02`
3. After terminal result, endpoint CMD: `call C:\temp\AegisSoftDistTests\endpoint-check.cmd 02`

Use two-digit numbers 01-34. Stop mockserver before every setup. The full guide repeats exact commands and expected outcomes for each case. Only the five Aegis-named fixture keys and three named result files are reset; no real applications are removed. CMD scripts require the fixed test path above.

The executable/certs do not belong in this folder. Keep mockserver.exe in its existing folder, and the service agent EXE in its existing service location. Use the supplied new build versions. No enrollment/TLS settings are cleared by the launcher.
