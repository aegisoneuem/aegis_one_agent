# Advanced SoftDist detection — server contract

Use `SoftwareDeploySpec.detection` with signed `Command.type=software_deploy_detect`.
This type covers detection alone, detection + bundle, or detection + bundle +
pre/post steps. Older agents must reject it; never downgrade to v2/bundle/workflow
and silently omit detection. Terminal action type remains `software_deploy`.

## Definition and API mapping

The console package editor sends its detection fields through the API. Backend
constructs `SoftwareDetection { mode, rules }`: `mode` is `all` (default) or `any`,
with 1–32 flat positive installed-state rules. No test files or environment
variables are required in production. Rule types and their fields:

| kind | Fields | Installed requirement |
|---|---|---|
| `file_exists` | `path` | A regular file exists |
| `file_version` | `path`, `minimum_version` | Regular file has native fixed FileVersion >= minimum |
| `registry_key` | `hive`, `key`, `view` | Key exists and is readable |
| `registry_value` | `hive`, `key`, `view`, `value_name`, `value_type`, `expected_value` | Value exists with required type and equals expected literal |

File paths must resolve to absolute local drive paths. UNC/device paths, relative
paths, traversal and mapped network drives are refused. FileVersion uses native
Win32 version-resource APIs, not PowerShell or a string ProductVersion. Minimum
versions have 1–4 unsigned numeric components (0–65535), padded with zeros.
Missing/unversioned/older output does not satisfy a version requirement.

Registry `hive` is `HKLM` or `HKCU`; `view` must be explicitly `32` or `64`.
`key` is a literal relative key path. `registry_value.value_name` may be empty for
the default value. `value_type` is `string`, `dword` or `qword`; numeric expectations
use unsigned decimal within the selected type's range. Strings compare exactly,
including case; REG_EXPAND_SZ is compared as stored, without environment expansion.
Unsupported/mismatched value types do not satisfy the condition.

Ordinary `${VAR}` substitution is supported in file paths and expected values;
unknown variables fail. Selector keys/hives/views/names and minimum versions remain
literal. Detection inputs are PUBLIC configuration, not secure credentials.
Secret values/passwords must not occur in any detection field. `${package_dir}`
is forbidden because a transient extraction directory cannot prove installation.

Do not mix advanced rules with legacy `product_code`, `uninstall_key`,
`display_name` or `version`: reject ambiguous definitions. Keep catalog/revision
metadata separate, and put file minimum version on its rule. Existing legacy
MSI/installed-product definitions remain supported under their prior task types.
Advanced uninstall uses an explicit command/file payload; this block does not
infer an automatic uninstaller from an arbitrary file or setting.

## Precheck, verification and removal

Rules run on the serialized worker, after execution session/profile selection.
This keeps filesystem/registry reads off the stream receiver. A file-based job
may already have been delivered/cached before its worker-side skip decision.

- Install: satisfied ALL/ANY group means NOT_APPLICABLE and no command/pre/post
  execution. Otherwise execute, then require the group to match after all steps.
- Successful exit code with an unsatisfied group becomes FAILED. Existing bounded
  verification attempts/interval are reused for delayed output/configuration.
- Uninstall: presence means ANY selected artifact exists, regardless of required
  version, value or ALL/ANY mode. Old/unversioned files, wrong-typed values and
  partly present groups must not cause a removal skip. Skip only if all selected
  artifacts are absent; verify removal only when all are absent afterward.
- For a value rule the artifact is the value, not its parent key. Include a
  `registry_key` rule too if the whole key's removal is required.
- Unreadable artifacts, unavailable selected-user profiles and invalid definitions
  fail detection. They are never silently interpreted as absent. All rules are
  read even when an earlier ALL/ANY result already determines compliance.

HKCU requires logged-in-user/specified-account execution with USER detection scope
(logged-in-user's default user scope also qualifies). It binds to the selected
token's loaded HKEY_USERS SID; never the agent service's own HKCU. File paths are
explicit installed locations and metadata is read by the agent service; this is
not a claim that file reads impersonate the installer user's ACL context.

## Dependencies and mock reference

`pkg/softdistplan` accepts advanced rules as a dependency's detection definition
and derives the new signed type. Each dependency retains its own execution scope,
parameters and rule set; failure blocks its parent as before.

For manual mock tests set `AEGIS_DEPLOY_DETECT_COUNT`, optional `DETECT_MODE`, then
indexed `AEGIS_DEPLOY_DETECT_<n>_KIND`, `PATH`, `MIN_VERSION`, `HIVE`, `KEY`, `VIEW`,
`VALUE_NAME`, `VALUE_TYPE`, `EXPECTED`. Indices are 1-based. Dependency fields use
`AEGIS_DEPENDENCY_DETECT_*` with the same suffixes. This maps to typed rules, not a
JSON test helper. Mock-only `DETECT_COUNT=0` explicitly disables custom rules
for standalone legacy tests, including inherited indexed fields. See [three manual cases](softdist-today-testing.md#d-advanced-detection).

Remaining boundaries: nested boolean trees, custom script-based detection,
file hashes/content matching, arbitrary registry operators, architecture solving
and production UI/persistence are not included. Real vendor package acceptance
remains separate from the fixture cases.
