# Installer branding assets

Drop your logo/branding files here with these **exact names, sizes, and formats**.
To rebrand later, replace the file and rebuild the MSI — nothing else changes.

| File | Purpose | Required size | Format |
|---|---|---|---|
| `banner.bmp` | Top banner on every wizard page | **493 × 58 px** | 24-bit BMP |
| `dialog.bmp` | Left image on Welcome + Finish pages | **493 × 312 px** | 24-bit BMP |
| `icon.ico` | Add/Remove Programs icon | 16/32/48 px frames | ICO |
| `license.rtf` | EULA shown on the License page (placeholder included) | — | RTF |

Notes
- Export the logos as **BMP** (not PNG/JPG) — WiX UI requires BMP. 24-bit, no alpha.
- Put the AegisOne / HCL logo on a clean background that matches the sizes above
  (the banner is short and wide; the dialog image is tall on the left).
- `icon.ico` should contain at least 16×16, 32×32, 48×48 frames for crisp display.
- These three image files are **not** in git yet — add them before `wix build`,
  or the build will fail on the missing `assets/*.bmp` / `assets/icon.ico`.
