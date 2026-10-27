# Multi-file SoftDist - server/console contract

Agent implementation and mock reference are implemented locally; real VM/vendor
acceptance remains pending. UI, upload wizard, catalog, scheduling and RBAC belong
to the server/console team. The wire format is native ZIP with a manifest.

## Author and publish

1. Accept multiple files/folders, preserving relative paths.
2. Create a ZIP using the format below. Native reference CLI:
   `mockserver.exe pack <source-folder> <new-bundle.zip>`.
3. Compute SHA256 of the completed ZIP and store it in the signed task's
   expected_hash. A source folder/output ZIP overwrite is refused by the reference
   packer; output must be outside the input folder.
4. Serve the ZIP using existing FetchContent kind=package with content_id. Agent
   verifies the hash, uses its encrypted cache, then extracts only manifested files.

Reference source: internal/softdist/bundle.go. The UI/server may implement the same
format in its own language. No endpoint internet download or external extraction
utility is involved.

## ZIP format

Root `aegis-package.json`:

```json
{
  "version": 1,
  "files": [
    {"path": "setup.msi", "size": 12345, "sha256": "<64 hex characters>"},
    {"path": "company-settings.mst", "size": 678, "sha256": "<64 hex characters>"},
    {"path": "settings", "size": 0, "directory": true},
    {"path": "settings/app.ini", "size": 42, "sha256": "<64 hex characters>"}
  ]
}
```

Each directory listed in the manifest has its corresponding ZIP directory entry.
Files use forward-slash relative paths and their actual byte size/SHA256. The
manifest itself is not listed in files and is not published into the extraction
folder. ZIP member order does not matter. Unlisted/missing members, case aliases,
file/directory collisions, traversal/absolute paths, links/reparse points and
Windows reserved paths are refused. The signed ZIP hash authenticates this manifest.

Default expanded size cap is 4 GiB; payload member cap is 10,000 (including
directories, plus the root manifest). ZIP directory metadata is capped at 16 MiB
and actual header counts are checked before allocating the archive reader. Windows
verification/extraction uses the same read-locked ZIP handle. Agent settings
AEGIS_SOFTDIST_BUNDLE_MAX_BYTES / AEGIS_SOFTDIST_BUNDLE_MAX_FILES control its limits;
the reference pack CLI uses the default limits. Manifest JSON is capped at 4 MiB.
Build-time source changes are detected by the reference packer instead of producing
an inconsistent bundle.

## Dispatch

Generate current bindings from sibling aegis_one_proto/grpc/agent.proto.
Sign Command.type=`software_deploy_bundle`, with SoftwareDeploySpec as its payload.
Older agents reject this distinct type. Never send bundle fields under legacy
software_deploy or software_deploy_v2; updated agents refuse that downgrade too.
Production targeting must select the updated bundle-capable build.

| SoftwareDeploySpec field | Meaning |
|---|---|
| content_id | ZIP in the package catalog |
| file_path | Optional pre-staged ZIP path; never the member entry point |
| expected_hash / hash_algorithm | Completed ZIP SHA256 / sha256 |
| installer_type | msi/exe/ps1/bat/cmd/python/vbs/auto, or command |
| silent_args | Package-author arguments; ordinary parameter substitution still works |
| bundle.entry_point | Relative manifested installer/script file, e.g. setup.msi |
| bundle.command | External command for installer_type=command, e.g. xcopy.exe |
| bundle.destination | Empty for managed staging; otherwise absolute NEW local folder |
| bundle.cleanup | Empty for defaults, always, or retain |
| execution / parameters / secure_parameters | Existing typed Run-As and parameter contract |

Use exactly one of entry_point or command. command is allowed only with type=command.
A command bundle still requires ZIP hash verification; it cannot bypass file delivery.
For a member entry, agent also rechecks its manifest SHA256 before execution.
require_signature applies to the member installer when using an installer/script
entry. Generic command jobs retain their existing signed-command trust semantics.

Example real MSI package settings (operator supplies genuine files):

- installer_type=msi
- bundle.entry_point=setup.msi
- silent_args=/qn /norestart TRANSFORMS=company-settings.mst
- bundle.destination empty for temporary/default extraction

All files must actually be in the package. This example is not a tested vendor
certification. Packages needing their source/MST for later maintenance should use
an appropriate retained custom location.

## Execution and paths

Default extraction: <AEGIS_DOWNLOAD_DIR>\packages\aegis-package-<random-id>.
Default download root is C:\Program Files\AegisOne\Client\SKVC\download.
Extraction directories have an explicit private DACL. A user-context job receives
read/execute access to package files; its process token/profile/environment are
preserved. The installer must use TEMP or an authorized output location for writes.
The agent does not grant that user access to SKVC credentials/keys.

The child working directory is the extracted package root. Relative MST/config/
supporting-file paths therefore resolve inside the package.
`${package_dir}` and AEGIS_PARAM_PACKAGE_DIR provide that root to package commands;
package_dir is reserved and cannot be overridden by author parameters. Quote
substituted paths when they contain spaces, e.g. "${package_dir}\config.xml".
Ordinary/secure parameters and user-scope detection continue to use their existing
contracts. Installer secrets remain stdin-only, never embedded by the packer.

## Retention and explicit cleanup

| Destination / cleanup setting | Outcome after command + verification |
|---|---|
| Managed default, empty or always | Remove owned extracted tree on success/failure |
| Custom destination, empty or retain | Keep the extracted tree |
| Custom destination, always | Remove owned extracted tree on success/failure |
| Managed default + retain | Refused: choose a custom destination for retention |

A custom destination must not exist, its parent must exist, and it must be a
local absolute clean path. No merge, overwrite, UNC share or existing-tree cleanup.
After verification the temporary ZIP decrypted from cache is removed before the
terminal result. An admin-provided pre-staged ZIP is not owned and is preserved.
The server's package catalog/source is unchanged by endpoint cleanup.

For a retained custom package, expose explicit file/folder cleanup actions using
the existing signed SYSTEM command facility or the user's NSH workflow. Examples:

```cmd
del /Q "C:\AegisPackages\FinanceApp\config.xml"
rmdir /S /Q "C:\AegisPackages\FinanceApp"
```

These are explicit author/operator commands, not an implicit blanket cleanup of
an existing directory. Validate/display the exact target in the console.

## Results

ActionStatus.action_type remains software_deploy. New typed fields:

- package_directory: actual owned extraction path, even after removal.
- package_cleanup: removed / retained / failed.

Stages are QUEUED, DOWNLOADING when fetched, VERIFYING while preparing, RUNNING
for execution, then the existing terminal phase. Mock output prints the path and
cleanup state. Cleanup failure makes the deployment FAILED with its reason;
installer exit/reboot flag and detection results remain available.

## Scope boundaries

Normal completion/failure cleanup is implemented. Abrupt process kill/power loss
has no durable bundle resume/orphan recovery journal in this block. Installers
that spawn children can leave files locked; timeout still does not guarantee
termination of every descendant. Such cleanup failures are reported, not hidden.
No automatic dependency chain, pre/post sequence or rollback is added here.
UI needs to distinguish executable completion, product detection, reboot and
cleanup instead of declaring every exit=0 a complete enterprise deployment.

Run [the three manual bundle cases](softdist-bundle-testing.md), then real MSI+MST,
EXE+config and user-context packages appropriate to your environment.
