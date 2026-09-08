# CLAUDE.md

Guidance for Claude Code when working in this repository.

## Overview

`azprofile` is a single Go CLI binary that manages multiple Azure CLI identities
on one machine. It keeps each identity in its own `AZURE_CONFIG_DIR`, switches
between them with a symlink, refreshes their tokens on a cron schedule, syncs
them between machines (rsync or encrypted Ably pub/sub), and activates Azure PIM
role assignments natively against ARM and the RBAC PIM API.

Module: `github.com/neverprepared/azprofile` (Go 1.26.2, see `go.mod`).
Dependencies: `spf13/cobra` (CLI), `ably/ably-go` (pub/sub), `zalando/go-keyring`
(OS keychain), `golang.org/x/term`. No Azure SDK — auth is delegated to the
user's `az` CLI.

## Architecture

```
cmd/azprofile/main.go        Cobra command tree — flag wiring only, no logic
internal/ui/                 ANSI colors, glyphs, ui.Die
internal/azprofile/          All behavior; each file owns one command group
  paths.go                   AZPROFILE_HOME, ~/.azure-profiles, ~/.azure symlink,
                             ValidateProfileName ([A-Za-z0-9._-] only)
  profile.go                 list/use/current/init/delete/login/whoami,
                             SetupWizard, JoinWizard, legacy ~/.azure migration
  refresh.go                 az account get-access-token per profile; EnsureCronPath
  cron.go                    crontab -l / crontab - editing, tagged lines
  doctor.go                  environment preflight checks (still warns when the
                             external az-pim-cli is absent — stale, PIM is native)
  update.go                  GitHub Releases self-updater (SHA256 + atomic replace)
  crypto.go                  AES-256-GCM helpers, key gen/hex/fingerprint
  keychain.go                master key: AZPROFILE_MASTER_KEY env > OS keychain
  syncconfig.go              ~/.config/azprofile/{config.enc,state.json}, atomicWrite
  envelope.go                gzipped JSON envelope of the synced files
  azureid.go                 UPN from azureProfile.json; sha256[:12] hash
  ablyclient.go / ablysync.go  channel naming, publish/receive/subscribe, dedupe
  sync.go                    rsync push/pull transport
  pim.go                     PIM command surface: list/active/activate/deactivate,
                             name resolution, ambiguity + close-name suggestions
  internal/azprofile/pim/    REST client (ported from netr0m/az-pim-cli):
                             client.go, models.go, token.go, utils.go, const.go
```

Key mechanics:

- **Profiles** live in `$AZPROFILE_HOME/.azure-profiles/<name>`. The active one
  is a symlink at `$AZPROFILE_HOME/.azure`. `AZPROFILE_HOME` defaults to `$HOME`
  and is honored everywhere (including `ConfigDir`), which is how tests and cron
  isolate state.
- **Sync payload** is only `msal_token_cache.json`, `azureProfile.json`,
  `clouds.config` (`SyncedFiles`). The first two are required. `msal_http_cache.bin`
  is deliberately excluded — it pushed messages past Ably's 64 KB limit.
- **Envelope**: JSON with `monotonic_seq`, sender id, UPN hash, base64 file
  contents and a SHA-256 checksum; gzipped, then AES-256-GCM sealed with the
  master key before it reaches Ably. Ably never sees plaintext.
- **Ably channel**: `<prefix>.<sha256(upn)[:12]>.<profile>` so the channel name
  does not leak the identity.
- **Auto-publish**: `Refresh`, `Init`, and `Login` call `PublishIfConfigured`,
  a best-effort hook that never fails the parent command.
- **Cron lines** are tagged (`# azprofile-refresh:<profile>`,
  `# azprofile-pim:<profile>`) and rewritten by filtering the tag out and
  re-appending. Absolute `os.Executable()` path, `${WORKSPACE_HOME:-$HOME}`
  resolved at run time. When `WORKSPACE_HOME` is set in the installing shell,
  a `WORKSPACE_HOME=<value>` line is written at the top of the crontab, since
  cron does not inherit it.
- **PIM auth** uses `az account get-access-token` for both the ARM scope and
  `https://api.azrbac.mspim.azure.com`, so the active azprofile identity is the
  principal. Three categories: `resource` (ARM), `role` (Entra `aadroles`),
  `group` (`aadGroups`).

## Commands

Build / install (Makefile):

```bash
make build                 # -> bin/azprofile (VERSION=dev unless overridden)
make install               # symlinks bin/azprofile into $PREFIX/bin (~/.local)
make install PREFIX=/usr/local
make uninstall
make clean
```

Verify — there is no lint config and no Makefile target for these; run them
directly:

```bash
go build ./...
go vet ./...
go test ./...             # tests: internal/azprofile, internal/azprofile/pim
gofmt -l .                # pre-existing: envelope.go, syncconfig.go, version.go
```

Run:

```bash
go run ./cmd/azprofile <args>
./bin/azprofile <args>
```

CLI surface (source of truth: `cmd/azprofile/main.go`):

```
list | use <name> | current | init <name> [--login --tenant --scope]
delete <name> | login [name] [--tenant --scope] | whoami
setup | doctor | refresh [profiles...] | update [--check --yes --force]
cron status
cron refresh install [profile] [schedule] | cron refresh remove [profile]
cron pim install <profile> <role>... | <profile> --all | cron pim remove [profile]
pim list|active [--type all|resource|role|group]
pim activate <name>... | --all [--type --role --duration --reason
                                --start-date --start-time --ticket-system
                                --ticket-number --wait --timeout --yes]
pim deactivate <name>... [--type --role]
sync push|pull [dir] [profile]            # rsync transport
sync publish|receive|subscribe [profile]  # Ably transport
sync join | keygen [--force] | import-key <hex> | export-key --confirm
sync configure --ably-key <k> [--channel-prefix p] | sync status
```

Environment: `AZPROFILE_HOME`, `AZPROFILE_SYNC`, `AZPROFILE_MASTER_KEY`,
`XDG_CONFIG_HOME`, `WORKSPACE_HOME` (cron).

## CI

`.github/workflows/release.yml` is the **only** workflow. It fires on `v*` tags
and cross-compiles darwin/linux × amd64/arm64 on `macos-15`, codesigns and
notarizes the darwin binaries, and publishes tarballs plus a
`*-checksums.txt` that `azprofile update` verifies against.

There is **no** build/test/lint workflow on push or pull request. Run the verify
commands above locally before opening a PR.

## Conventions

- `cmd/azprofile/main.go` only defines cobra commands and copies flags into an
  options struct; every behavior lives in `internal/azprofile`. Keep it that way.
- Commands return `error`; `main` funnels them into `ui.Die`. `SilenceUsage` and
  `SilenceErrors` are set, so error strings are the user-facing message.
- Use `ui.Green`/`ui.Check`/`ui.Dim`/etc. for output. They are empty strings when
  stdout is not a TTY, so no escape codes leak into logs or pipes.
- Never print the master key or the Ably API key. `export-key` requires
  `--confirm`; `sync status` and `doctor` truncate the Ably key to 8 chars.
- Config and state are written with `atomicWrite` (temp file + `Rename`) at
  `0600`, under a `0700` directory.
- Validate any profile name that reaches a path or a cron line with
  `ValidateProfileName`, and shell-quote cron arguments with `shellQuote`.
- Paths always go through `Home()` / `ProfilesDir()` / `ProfilePath()` so
  `AZPROFILE_HOME` keeps working.
- Version is injected at build time via
  `-ldflags "-X '.../internal/azprofile.Version=<tag>'"`; it stays `dev` locally,
  and `update` refuses to replace a dev install without `--force`.
- Tests are plain `testing` table tests next to the code, with no network calls —
  keep new tests offline.
