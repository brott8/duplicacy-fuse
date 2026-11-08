# duplicacy-fuse

`duplicacy-fuse` is a read-only FUSE2 view of a selected Duplicacy snapshot revision. It also lists file or exact-directory history for scripting and future desktop integrations.

## Lineage

This private GitHub repository continues the original implementation at [GitLab](https://gitlab.com/andrewheberle/duplicacy-fuse). The original Git commit history is preserved.

## Requirements

On Manjaro, install Go, GCC, pkgconf, and the FUSE2 development/runtime package:

```sh
sudo pacman -S go gcc pkgconf fuse2
```

Install the Duplicacy CLI separately and ensure `duplicacy` is on `PATH`. User mounts require access to `/dev/fuse` and the `fusermount` helper provided by FUSE2.

Build the Linux FUSE2 binary from the repository:

```sh
make build-linux
```

The build uses cgofuse v1.6.0's default FUSE2 API. FUSE3 can be selected manually with `-tags=fuse3` on systems whose FUSE3 library has the `libfuse3.so.3` soname; the current Manjaro package uses `libfuse3.so.4` and is not detected by this cgofuse version.

## History

The repository path is explicit. If exactly one storage is configured it is selected automatically; otherwise pass `--storage` to avoid choosing the wrong remote. The configured snapshot ID is used unless `--snapshot` is provided.

File history uses Duplicacy's `history` command:

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

Pass a positive revision and a repository-relative selected path. If `--path` names a file, the FUSE root contains only that file; if it names a directory, the mounted root is that directory and contains only its subtree. Omitting `--path` exposes the repository root. All mounts are read-only and run in the foreground.

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

Use `duplicacy-fuse revisions` or file history to discover revisions. These commands invoke an external Duplicacy CLI; use an SMB-capable CLI and pass `--duplicacy-bin` when it is not on `PATH`. Mounting uses the Duplicacy Go library directly. When multiple storages are configured, `--storage` is required. `--password` is the Duplicacy storage-encryption password, not the SMB login password. Direct `smb://` storage is supported; its password is read from Duplicacy's saved `smb_password` preference/keyring or `DUPLICACY_<STORAGE_NAME>_SMB_PASSWORD` (for example, `DUPLICACY_UNRAID_SMB_PASSWORD`). SMB reads retry once after recognized transport or expired-session errors when no partial chunk has been received; if reconnecting fails, remount to start a fresh session. Avoid putting either password in process arguments.

Unmount from another terminal:

```sh
fusermount -u /mnt/duplicacy-history
```

The mount process exits after unmount. The source repository is never used as a destination for restored file data.

FUSE chunk reads support Duplicacy's password-based storage encryption. RSA-encrypted file chunks are not supported because this application does not load an RSA private key and currently fail with a read error.

## Chunk Cache

Requested chunks are fetched on demand and stored as plaintext outside the repository, by default under `${XDG_CACHE_HOME:-$HOME/.cache}/duplicacy-fuse/chunks`. Cache files are created with owner-only permissions; protect custom cache locations accordingly. `--cache-dir` selects another location and must resolve outside the repository. `--cache-size` sets the maximum retained payload size; LRU eviction removes old chunks. A zero size disables persistent caching. Concurrent requests for the same chunk share one download. Reads use independent in-memory copies, so an evicted disk entry cannot invalidate an active read.

Clear the default chunk cache with:

```sh
./duplicacy-fuse clear-cache
```

For a custom cache location, pass the same directory with `--cache-dir`. The command only clears directories marked as a `duplicacy-fuse` cache.

Revision manifests and chunk indexes are metadata and are loaded once per mount. File data is not prefetched or staged; each read fetches only chunks intersecting its requested byte range. Selected historical views do not support writes, restores, rename inference, or full-repository browsing across revisions.

## Testing

The repository contains separate Go modules. Run tests in both:

```sh
go test ./...
(cd dpfs && go test ./...)
go test -race ./...
(cd dpfs && go test -race ./...)
```

Build and mount integration tests require a Go 1.17+ toolchain, FUSE2 development headers, a usable Duplicacy repository, and a user-level FUSE mount environment.
