# AegisOne Client installer (MSI)

WiX v4/v5 installer for the AegisOne agent: installs the binary, registers the
`AegisOneAgent` Windows service (LocalSystem, auto-start, restart-on-failure),
writes `config.json`, and installs the CA cert — interactively or silently.

## Prerequisites (on the build machine)

1. **.NET SDK** (6.0+) and the **WiX toolset**:
   ```
   dotnet tool install --global wix
   wix extension add -g WixToolset.UI.wixext
   wix extension add -g WixToolset.Util.wixext
   ```
2. A **built agent**: `build\agent-windows.exe` (the .wxs references `..\build\agent-windows.exe`).
3. **Branding assets** in `assets/` — see [assets/README.md](assets/README.md)
   (`banner.bmp`, `dialog.bmp`, `icon.ico`; `license.rtf` placeholder included).
4. For signing: an **Authenticode code-signing certificate** (OV or EV) + the
   Windows SDK `signtool.exe`.

## Build the MSI

From this `installer/` folder:
```
wix build Package.wxs ConfigDlg.wxs ^
  -ext WixToolset.UI.wixext -ext WixToolset.Util.wixext ^
  -o AegisOneClient.msi
```

## Sign the agent exe AND the MSI (Authenticode = B7 self-integrity)

Sign the **exe before** building the MSI (so the embedded binary is signed), then
sign the **MSI** after. Use a timestamp URL so signatures outlive cert expiry.
```
rem 1) sign the agent binary first
signtool sign /fd sha256 /tr http://timestamp.digicert.com /td sha256 ^
  /f AegisCodeSign.pfx /p <PFX_PASSWORD> ..\build\agent-windows.exe

rem 2) build the MSI (above), then 3) sign the MSI
signtool sign /fd sha256 /tr http://timestamp.digicert.com /td sha256 ^
  /f AegisCodeSign.pfx /p <PFX_PASSWORD> AegisOneClient.msi

rem verify
signtool verify /pa /v AegisOneClient.msi
```
EV certs live on a hardware token/HSM — use `/n "<Subject>"` or `/sha1 <thumbprint>`
instead of `/f ... /p ...`. Never commit the .pfx or its password.

## Interactive install

Double-click the MSI → Welcome → License → **Install Location** → **Connection &
Enrollment** (server address, server cert name, enrollment token, CA cert path) →
Ready → Progress → Finish. Config is written and the service starts automatically.

## Silent install (BigFix-style)

Pass the same settings as properties:
```
msiexec /i AegisOneClient.msi /qn ^
  SERVERADDRESS=aegis.corp.local:50051 ^
  SERVERNAME=aegis.corp.local ^
  ENROLLTOKEN=tenant-abc-123 ^
  CACERTFILE=C:\deploy\ca.crt ^
  INSTALLFOLDER="C:\Program Files\AegisOne\" ^
  /l*v install.log
```
For a config-file-driven deploy, keep a per-tenant `.cmd` (or MST transform) that
calls the line above — that is the "response file" equivalent.

Silent uninstall:
```
msiexec /x AegisOneClient.msi /qn
```

## What the installer does at install time

1. Copies `agent-windows.exe` to `<INSTALLFOLDER>\Client\`.
2. Runs `agent-windows.exe writeconfig --install-dir "<..>\Client" --server .. --server-name .. --token .. --ca ..`
   → writes `SKVC\config.json` and copies the CA cert to `SKVC\certs\ca.crt`.
3. Registers + starts the `AegisOneAgent` service (restart-on-failure).

On first run the agent generates its device identity and **enrolls** (keypair +
CSR → server signs → per-device cert), then connects over mTLS.

## Uninstall / data

Uninstall stops + removes the service and deletes the program files. The `SKVC`
data (identity, certs, cache) handling is intentionally left to policy — by
default MSI removes files it installed but may leave created data; for a full
wipe on decommission, delete `SKVC` separately or add a `RemoveFolder`/custom
action. (Keeping identity across reinstall is fine — the server reconciles by
hardware fingerprint regardless.)

## Fallback if the custom dialog misbehaves

WiX dialog reflow is version-sensitive. If `AegisConfigDlg` navigation gives
trouble, build with only `Package.wxs` (drop `ConfigDlg.wxs` and the
`<ui:WixUI>` stays `WixUI_InstallDir`): the interactive install then collects the
install location only, and server/token/CA come from **properties** (silent) or
by editing `SKVC\config.json` after install. The MSI stays fully functional.
