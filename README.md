# duplicacy-fuse

`duplicacy-fuse` is a read-only FUSE2 view of a selected Duplicacy snapshot revision. It also lists file or exact-directory history for scripting and future desktop integrations.

## Lineage

This private GitHub repository continues the original implementation at [GitLab](https://gitlab.com/andrewheberle/duplicacy-fuse). The original Git commit history is preserved.

## License and Acknowledgments

The `duplicacy-fuse` code, including the in-tree `dpfs` module, is licensed under the MIT license in [`LICENSE`](LICENSE). The original `dpfs` implementation was also MIT-licensed. [`cgofuse`](https://github.com/winfsp/cgofuse) is MIT-licensed under its [upstream license](https://github.com/winfsp/cgofuse/blob/v1.6.0/LICENSE.txt). These terms apply to their respective code; other dependencies retain their own licenses.

Duplicacy's embedded Go library and CLI are distributed under [Duplicacy's license](https://github.com/gilbertchen/duplicacy/blob/c8530c4d0fd4b607fd3e0fd98db359f6d7a15a19/LICENSE.md), not the MIT license. Its terms allow personal use and commercial trials; non-trial commercial use generally requires a per-computer CLI license, and commercial use of derivative works is subject to the same requirements. This project is intended to remain free and open source and is not offered commercially. The upstream terms may still apply to users and redistributors; consult them before any commercial use. Preserve applicable third-party license notices when redistributing builds.

Thanks to [Gilbert Chen and the Duplicacy contributors](https://github.com/gilbertchen/duplicacy) for Duplicacy; [Bill Zissimopoulos and the WinFsp/cgofuse contributors](https://github.com/winfsp/cgofuse) for the Go FUSE bindings; and [Andrew Heberle](https://gitlab.com/andrewheberle/duplicacy-fuse) for the original `duplicacy-fuse` implementation and `dpfs` code.

## Requirements

On Ubuntu, install the Go, C, pkg-config, and FUSE2 build/runtime dependencies:

```sh
sudo apt update
sudo apt install -y golang-go build-essential pkg-config libfuse-dev fuse
```

The Go modules require Go 1.26 or newer. Check `go version`; Ubuntu's `golang-go` package can be older, in which case install a current Go release from [go.dev/dl](https://go.dev/dl/) before building. Build the Linux FUSE2 binary with:

```sh
make build-linux
```

The build uses cgofuse v1.6.0's default FUSE2 API. Mounting uses the embedded Duplicacy Go library and does not require the Duplicacy CLI. File history, revision listing, and directory-history revision discovery invoke the external Duplicacy CLI; install an SMB-capable version and put it on `PATH` or pass `--duplicacy-bin`. User mounts require access to `/dev/fuse` and the `fusermount` helper provided by FUSE2.

The CLI manual is in [`man/duplicacy-fuse.1`](man/duplicacy-fuse.1); read it with `man ./man/duplicacy-fuse.1`. Each command also accepts `--help` for a concise flag listing.

## History

The repository path is explicit. If exactly one storage is configured it is selected automatically; otherwise pass `--storage` to avoid choosing the wrong remote. The configured snapshot ID is used unless `--snapshot` is provided.

File history delegates to Duplicacy's native `history` command. Revision listing delegates to `duplicacy list`; directory history also uses `list` to discover revisions, then the embedded library reads each revision's manifest to test whether the exact directory exists. This split reuses Duplicacy's established history and revision-listing behavior instead of reimplementing it, while directory manifests and mounted file data are read directly through the library.

File history example:

```sh
./duplicacy-fuse history --repo /path/to/repo --storage nas --path 'src/main.go'
./duplicacy-fuse history --repo /path/to/repo --storage nas --path 'src/main.go' --json
```

Directory history discovers revisions with `duplicacy list`, then reads revision manifests and reports only revisions where that exact directory exists. It does not download file contents:

```sh
./duplicacy-fuse history --repo /path/to/repo --storage nas --path 'src' --directory --json
```

Discover snapshot revisions and creation times with the CLI-backed `revisions` command:

```sh
./duplicacy-fuse revisions --repo /path/to/repo --storage nas --json
```

JSON output is suitable for shell tools or a later Nautilus context-menu caller. File history records include revision, size, timestamp, hash when present, path, and the original CLI row. Directory records include revision, snapshot timestamp, path, and presence.

## Mount

Pass a positive revision and an optional repository-relative selected path. If `--path` names a file, the FUSE root contains only that file; if it names a directory, the mounted root is that directory and contains only its subtree. Omitting `--path` exposes the repository root. Repeating `--revision` or `--path` creates a comparison mount: each revision with at least one matching selected path gets a top-level `revision-N-YYYY-MM-DD-HHMMSSZ` folder, and selected paths retain their repository-relative hierarchy inside it. Use `--compare` to force this layout for a single revision and path. A selected directory includes its subtree. Missing selected paths are omitted individually; revisions with no matching paths are omitted, and the mount fails if nothing matches. Paths are compared exactly as stored, without rename inference. All mounts are read-only and run in the foreground by default. On Linux, `--background` starts a detached mount and returns after FUSE initialization; it prints the path to a mode-0600 log file under the system temporary directory.

For current-format snapshots mounted at the repository root, directory entries are streamed and cached when a directory is first listed, rather than building the full file tree at mount startup. Explicit `--path` views still load metadata up front. Legacy JSON-format snapshots must be sorted by Duplicacy before listing, so their first directory browse still loads all metadata. The snapshot format has no random-access index, so first access to a deep directory may scan preceding metadata entries.

```sh
./duplicacy-fuse mount \
  --repo /path/to/repo \
  --storage nas \
  --snapshot repo-id \
  --revision 42 \
  --path 'src' \
  --mountpoint /mnt/duplicacy-history \
  --cache-size 10GiB
```

Compare selected paths across revisions:

```sh
./duplicacy-fuse mount --compare \
  --repo /path/to/repo \
  --storage nas \
  --revision 41 --revision 42 \
  --path 'src/main.go' --path 'docs' \
  --mountpoint /mnt/duplicacy-comparison
```

Comparison setup loads metadata for each requested revision, so startup time and metadata memory use grow with the number and size of selected revisions. File chunks remain lazy and are fetched only when read.

Use `--debug` on `mount` to log setup timings, entries scanned per directory, and chunk read/cache timing. Duplicacy INFO/TRACE logs are suppressed in debug, background, and desktop-helper modes to avoid exposing key material; warnings and errors remain visible. Debug output may contain repository paths and directory names, so review it before sharing.

Use `duplicacy-fuse revisions` or file history to discover revisions. File history and revision listing use an external Duplicacy CLI; directory history uses it for revision discovery, then uses the embedded library for manifest checks. Mounting uses the embedded Duplicacy Go library for snapshot metadata and on-demand chunk reads, which provide the direct access needed by the read-only FUSE filesystem. Pass `--duplicacy-bin` to select the CLI executable when it is not on `PATH`. CLI-backed operations run with the selected repository as their working directory and use the CLI's normal configuration and behavior. When multiple storages are configured, `--storage` is required. `--password` is the Duplicacy storage-encryption password, not the SMB login password. Direct `smb://` storage is supported; its password is read from Duplicacy's saved `smb_password` preference/keyring or `DUPLICACY_<STORAGE_NAME>_SMB_PASSWORD` (for example, `DUPLICACY_UNRAID_SMB_PASSWORD`). SMB reads retry once after recognized transport or expired-session errors when no partial chunk has been received; if reconnecting fails, remount to start a fresh session. Avoid putting either password in process arguments.

Unmount from another terminal:

```sh
fusermount -u /mnt/duplicacy-history
```

The mount process exits after unmount. The FUSE view rejects file writes and never restores file contents into the source repository, but that does not guarantee zero backend-side effects. Snapshot loading calls Duplicacy's `CreateDirectory` for `snapshots/<snapshot-id>` (the directory should already exist for a valid revision). Use a disposable storage copy if the backend must remain strictly unchanged. File history, revision listing, and directory-history revision discovery invoke the external CLI with its normal configuration and hook behavior; directory-history manifest checks and the mounted view use the embedded library.

Archived symlinks are exposed with their original targets. Absolute or `..` targets may resolve outside the mount in the host filesystem, so this mount is not a security sandbox.

FUSE chunk reads support Duplicacy's password-based storage encryption. RSA-encrypted file chunks are not supported because this application does not load an RSA private key and currently fail with a read error.

## GNOME Companion

The GNOME desktop client is maintained as a separate companion project. It uses the optional `gui-storages`, `gui-snapshots`, `gui-revisions`, `gui-browse`, and `--gui-control-fd` helper interfaces; this CLI remains independently buildable and does not depend on GTK or libadwaita. Comparison profiles choose a reference revision to browse and one or more comparison revisions; selected repository-relative paths are applied exactly to each comparison revision. GUI-managed encrypted operations ignore saved/environment storage-encryption passwords and request a fresh password over a private inherited Unix socket. The desktop app never sends that password in command-line arguments or environment variables. Snapshot IDs, revisions, and browsed directory entries are read through the embedded library and do not require the external Duplicacy CLI.

## Chunk Cache

Requested chunks are fetched on demand and stored as plaintext outside the repository, by default under `${XDG_CACHE_HOME:-$HOME/.cache}/duplicacy-fuse/chunks`. Cache files are created with owner-only permissions; protect custom cache locations accordingly. `--cache-dir` selects another location and must resolve outside both the repository and backup storage; the application checks only that it is outside the repository. `--cache-size` sets the maximum retained payload size; LRU eviction removes old chunks. A zero size disables persistent chunk-payload caching. Concurrent requests for the same chunk share one download. Reads use independent in-memory copies, so an evicted disk entry cannot invalidate an active read.

Clear the default chunk cache with:

```sh
./duplicacy-fuse clear-cache
```

For a custom cache location, pass the same directory with `--cache-dir`. The command only clears directories marked as a `duplicacy-fuse` cache.

Revision manifests and chunk indexes are metadata and are loaded once per mount. File data is not prefetched or staged; each read fetches only chunks intersecting its requested byte range. Historical views do not support writes, restores, or rename inference. Comparison mounts expose only selected exact paths (or the whole revision when no path is selected).

## Testing

The repository contains separate Go modules. CI runs tests in both modules, including race tests, and builds the Linux binary. Tests are hermetic: they do not mount FUSE or require live backup storage or credentials. The dependency regression suite exercises Duplicacy's embedded library against a local fixture and checks the external CLI invocation contract with a fake executable.

The weekly dependency workflow checks the latest official Duplicacy release, updates the same pinned source commit in both modules, and opens a PR only after tests, race tests, and the Linux build pass. It does not auto-merge. The workflow is also available for manual dispatch. Repository Actions settings must allow workflows to create pull requests. The external CLI used for history and revision commands is independently versioned and is not updated by this workflow.

Run tests locally in both modules:

```sh
go test ./...
(cd dpfs && go test ./...)
go test -race ./...
(cd dpfs && go test -race ./...)
```

Build using the Go version required by the root and `dpfs` module files, plus GCC, pkg-config, and FUSE2 development headers. CI and the dependency-update workflow use Go 1.26 because the current upstream Duplicacy release requires it; accepting that dependency update will raise the project minimum. Running a mount additionally requires a usable Duplicacy repository, a user-level FUSE environment, and access to `/dev/fuse`. CLI-backed history and revision commands also require an SMB-capable Duplicacy CLI; mounting does not.
