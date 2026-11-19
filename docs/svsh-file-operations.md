# SVSH file operations and host search

24 October 2026. Requires the updated **agent and mockserver**; old agents do not
implement file_operation. These commands run as the agent's service identity
(Windows LocalSystem). File content is copied as bytes, never executed.

## Find a device without listing the fleet

```text
search win11
search 9094704080
hosts --status online
hosts host=server --status offline --page 1 --limit 20
hosts --page 2
cd \\WIN-TUES4V46T7I
```

hosts/search show 20 rows by default, at most 100 with --limit, and include
ONLINE/OFFLINE status, total matches and a next-page command. Filters search
hostname/device_id/computer_id; host=, id= and cid= restrict the field.
No list is printed automatically on session open or right-click. You can select
a known device directly without running hosts. Duplicate hostnames are refused:
select its device_id or computer_id instead. An offline selection is allowed;
operations fail immediately until that endpoint reconnects. They do not wait
for other devices or a heartbeat. The mock scans its in-memory registry to build
a page; production needs indexed/paginated authorized inventory queries. The
50k simulated-record test verifies bounded results, not 50k live connections.

## Device paths

```text
\\srv1\c\temp\loop.txt
\\srv1\c$\temp\loop.txt
\\vm1,vm2\d\drop\
\\*\c\temp\drop\
```

These are **SVSH managed-device paths**, not SMB share access. c/c$ means C:,
d/d$ means D:. Hosts must be enrolled agents known to the server. A comma list
targets those devices; * snapshots currently online devices. Local drive or
relative paths apply to the currently selected target(s) and per-device CWD.
Quote the entire path when it contains spaces. An unmanaged SMB file server is
not reachable through this syntax: native run/PowerShell SMB commands remain
subject to the endpoint service account's actual network permissions.

## Copy and move

```text
copy \\srv1\c\temp\loop.txt \\mockserver\c\temp\
copy \\srv1\c\temp\loop.txt \\vm1,vm2\c\temp\
copy -r \\srv1\c\temp\reports \\vm1,vm2\c\backup\
copy \\srv1\c\temp\*.txt \\vm1\c\backup\
copy --overwrite \\srv1\c\temp\loop.txt \\vm1\c\temp\loop.txt
move \\srv1\c\temp\loop.txt \\vm1\c\archive\
```

cp/mv are aliases. A trailing backslash or existing directory means a destination
folder; a new destination without a trailing slash can rename a single file.
Directories require -r/--recursive. Directory copy places the source directory
under the destination. Empty directories and binary bytes are preserved. Links
are refused. This is content copy, not a backup preserving ACLs/ownership/ADS.

Existing files are refused unless --overwrite is supplied. Files may be copied
within one machine or across agents. A many-source to one-destination copy adds
<source-host>-<computer_id> subfolders to avoid source filename collisions (device
ID is the fallback). One-source distribution exports once then delivers to the
destinations with bounded workers. No Windows-share credentials are needed.

move supports exactly one source host and one destination host. It copies and
verifies the destination first, rechecks all source hashes and only then removes
source files; source changes stop removal. It is not a distributed atomic rename:
a failure/cancellation can leave copied files and/or remaining source files.
Do not automatically retry a partial move. Concurrent writers should be stopped
for a move; hash rechecks cannot make the whole cross-device job transactional.

## Bulk create and delete

```text
mkdir \\vm1,vm2\c\temp\new-folder
touch \\vm1,vm2\c\temp\new-folder\empty.txt
delete \\vm1,vm2\c\temp\old.txt
delete \\vm1,vm2\c\temp\*.log
delete -r \\vm1,vm2\c\temp\obsolete-folder
```

md aliases mkdir; del/rm alias delete. mkdir creates missing parents. touch
creates a new empty file and refuses an existing file. Directory deletion
requires --recursive. File/glob matches and per-host results are explicit;
missing files and permission failures report FAILED. File jobs update SVSH $exit
with the aggregate result. Copy does not execute an EXE/script/SVSH file.

Native shell commands remain available: Get-ChildItem, Copy-Item, Remove-Item,
echo text > file, etc. Use `run copy /Y ...` or `run del /Q ...` if you want the
Windows command's own syntax rather than SVSH's copy/delete flags. Native UNC
access uses the selected endpoint's service credentials; it is not the SVSH relay.

## Bounds and transfer contract (server team)

```text
set file-workers 4
set file-max-bytes 524288000
set file-max-files 10000
set timeout 300
```

Defaults: 4 destination workers (configurable 1..32), 500 MiB uncompressed data
per bundle, 10,000 entries, existing SVSH command timeout. Signed manifest metadata
must fit 2 MiB; larger sets fail and require smaller batches. Rate throttling
remains backlog. Recursive/glob copy needs no user-authored foreach loop.

New extensible command: **file_operation**, with a signed JSON payload defined
by internal/fileops.Spec, version=1. It uses the existing Command envelope,
ActionStatus, UploadContent and FetchContent RPCs; no protobuf regeneration.
Agent handler: cmd/agent/fileoperation.go. Operations: pack, unpack, copy, move,
mkdir, touch, delete and remove-manifest. The last operation is for the server's
post-copy move step only. Local copy/move use native staging on that endpoint.

Cross-agent flow: signed pack -> source ZIP upload -> server-verified upload
receipt -> temporary destination-bound content ID -> signed unpack -> existing
mTLS FetchContent(kind=svsh-file) -> archive SHA-256 and signed per-file hashes
verified -> destination publish -> optional source removal. Entry names, sizes,
counts, hashes, path traversal and link destinations are validated. No-clobber is
the default. Server never trusts a client-provided server filesystem path as the
relay source. Content access is bound to destination device and archive digest.
All writes on a device's Connect stream are serialized, including mock startup
commands. Jobs are correlated by action ID and report partial failures honestly.

Temporary archives are removed on normal completion; crash cleanup/retention
needs production handling. Destination publication across multiple files/hosts
is not transactional. Transfer artifacts must be tenant-scoped in production.

Mockserver has no console operator RBAC. The real server must authorize every
source read and destination write/delete, including expanded wildcard targets;
an SVSH-open permission alone must not grant unrestricted file writes. Audit the
operator, parent job, source/destination device IDs/paths, recursion/overwrite,
child action IDs, bytes and results. Reuse the console login identity for these
checks; endpoint execution still remains LocalSystem. Apply quotas and bounded
fanout instead of executing arbitrary browser-authored JSON directly.

## Acceptance tests

1. Copy a known binary and text file across two VMs; compare SHA-256 on both.
2. Distribute to two targets; verify each copy and retain the source.
3. Test recursive directories, empty directories, globs and paths with spaces.
4. Existing destination fails; --overwrite succeeds only when explicitly used.
5. Test move, source changed during copy, offline target and partial failures.
6. In a disposable test folder, bulk mkdir/touch/delete and recursive delete.
7. Search/paginate a large inventory and test online/offline/ambiguous names.
8. Confirm right-click/device selection never dumps a host list automatically.

Automated native/relay/orchestration/authorization and 50k inventory-page tests
are included. Real multi-VM acceptance for this new feature is still pending.
