# SoftDist completion checklist

Agreed with Vijay, 25 October 2026: finish Software Distribution before starting
other module tracks. A runnable engine alone is not a claim of complete product
parity. Close a block with actual acceptance results before calling it complete.

## 1. Current block: machine-wide uninstall and custom payload acceptance

- Run [the concrete acceptance cases](softdist-uninstall-edge-cases.md) and record
  results per endpoint in [the checklist](fixtures/softdist/softdist-results.md).
- Test 01 (ambiguous versions) passed on host mockserver. Other fixture, real
  MSI/EXE and new NSH Runner cases remain pending until executed.
- Fix failures and repeat the affected cases. Preserve quiet/silent arguments,
  exact product identity, pinned file integrity and honest post-verification.

## 2. Next coding block: Run-As and per-user applications

- Define SYSTEM, logged-in-user and specified-account execution semantics.
- Bind execution to the intended user/session, with token/profile/environment
  handling and secure credential delivery/storage where applicable.
- Add matching per-user detection and verification (HKCU/HKU and MSI context).
- Test no logged-in user, multiple sessions, unavailable credentials, permissions,
  timeout and reconnect behavior. Reading HKCU alone is not user-context removal.

## 3. Package authoring, variables and automation

- Complete reusable package definitions with explicit install/uninstall commands,
  author arguments, static and endpoint-resolved parameters and variables.
- Existing substitution covers command/arguments and child-script environment;
  do not claim every package field supports substitution without implementing and
  testing that field.
- Validate custom native/script payloads and the shared NSH Runner workflow in
  the real Software Distribution caller, including parameters, target scope,
  failure propagation, artifact/result capture and concurrent-job isolation.
- Close multi-file package/manifest handling, supporting files/MST/configs,
  advanced detection and dependency/pre/post sequencing gaps as agreed per block.
- Separate installed-version comparison from package revision/version control,
  promotion and downgrade/rollback behavior. Define and verify the required rules.

## 4. Production workflow and acceptance

Server/console team owns the production backend/UI, authorization, package
library, targeting, scheduling/rings/approvals, retry/job persistence, progress
and audit/result views. Track their completion and test the entire path; source
access/reference mock code does not prove this wiring is complete.

Close reboot-sensitive verification and remaining installer edge cases, then
exercise the supported deployment matrix and agreed fleet/network scenarios.
Document unsupported cases explicitly rather than declaring unknown results
successful. User-facing notifications/self-service and other lifecycle features
need an explicit supported scope before claiming enterprise-product equivalence.

## Deferred tracks

Full patching/catalog expansion, bandwidth throttling, peer-cache, roaming
detection and unrelated modules stay queued while SoftDist is the active focus.
Their agreed sequence remains recorded in project memory. Delivery/scale claims
must continue to reflect the capabilities actually implemented and tested.
