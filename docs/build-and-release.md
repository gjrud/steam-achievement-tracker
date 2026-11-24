# Build, packaging and release

This document covers how the app is built, packaged and released: prerequisites, local build commands, packaging files, the CI and release workflows, and the release procedure ([Cutting a release](#cutting-a-release)). The user-facing install steps are in the [README](../README.md#install). The authoritative [clean-checkout verification sequence](testing.md#clean-checkout-full-verification) is in testing.md; frontend build details are in [frontend.md](frontend.md).

## Prerequisites

Development requirements. CI and the release workflow pin matching versions where possible (see [Upgrading Wails](#upgrading-wails)).

- Go 1.27.4 or newer (`go.mod`). The Go image in `build/docker/Dockerfile.cross` uses `golang:1.27-trixie`.
- Node.js 22.22.2 or newer on the 22.x line, 24.15 or newer on 24.x, or 26 or newer, with npm. Vite 8 alone needs 20.19+ or 22.12+; the test tooling (Vitest 5, jsdom 31) sets the stricter floor. CI uses Node 22.
- The Wails v3 CLI, `wails3`, at the version in `go.mod` (`v3.1.0`):

  ```sh
  go install github.com/wailsapp/wails/v3/cmd/wails3@v3.1.0
  ```

- Linux build dependencies for Wails and CGO: a C compiler, `pkg-config`, the GTK 4 development headers and the WebKitGTK 6.0 development headers. On Debian and Ubuntu: `build-essential pkg-config libgtk-4-dev libwebkitgtk-6.0-dev`.
- CGO for `github.com/mattn/go-sqlite3`. It compiles its own bundled SQLite, so only a C compiler is needed, not a system SQLite library.
- For real sync testing on either OS: a system keyring (Secret Service on Linux), a Steam Web API key, network access, and the key owner's own Steam profile (any privacy setting). The same applies to the compiled app.
- Windows: Go, Node.js 22 and the Wails CLI as above; MinGW-w64 `gcc` on `PATH` for CGO; NSIS (`makensis`) for the installer. The Windows build runs only on a Windows host. Cross-compiling from Linux or macOS is not supported.

Run `wails3 doctor` to check the local Wails dependencies.

## Local builds

Run these commands from the repository root. Wails handles frontend installation, the frontend build and bindings generation (see [Task files](#task-files)).

```sh
wails3 dev     # development mode: runs the app with the Vite dev server
wails3 build   # production binary: bin/steam-achievement-tracker (bin\steam-achievement-tracker.exe on Windows)
```

`wails3 build` and `wails3 package` build for the current OS only. `wails3 task --list-all` lists every task.

Linux packages (AppImage, deb, rpm and Arch) go to `bin/`:

```sh
VERSION=0.0.0-dev wails3 package
```

Packages need a version; see [Version handling](#version-handling). A bare `bin/steam-achievement-tracker` runs, but has no menu entry or icon until a package installs it.

The deb, rpm and Arch packages declare the GTK 4 and WebKitGTK 6.0 runtime packages as dependencies (see `build/linux/nfpm/nfpm.yaml`). They do not depend on a Secret Service provider, so install one separately. The AppImage step downloads `linuxdeploy`, so it needs network access.

Windows (run on a Windows host with the dependencies above). The version is `VERSION` if set, otherwise the git tag on the checked-out commit, so on a tagged commit you can skip the next step. To set it explicitly, in PowerShell:

```powershell
$env:VERSION = "0.0.0-dev"
wails3 task windows:build                  # bin\steam-achievement-tracker.exe (GUI subsystem, no console)
wails3 task windows:create:nsis:installer  # bin\steam-achievement-tracker-amd64-installer.exe, from the existing exe
wails3 task windows:package                # build + installer
```

`VERSION` must start with `X.Y.Z`. Pre-release suffixes (`1.2.3-rc.1`) and a leading `v` are accepted, but Windows resources and NSIS take only the numeric core plus `.0`, so `1.2.3-rc.1` becomes `1.2.3.0`. The full tag name belongs in release filenames, which the release workflow sets. `windows:build` needs a version unless `DEV=true`; `windows:create:nsis:installer` always needs one (see [Version handling](#version-handling)). `windows:create:nsis:installer` does not rebuild the exe. It writes the WebView2 bootstrapper (embedded in the Wails CLI) into `build/windows/nsis`, which git ignores.

Generated bindings: after adding, removing or changing an exported `App` method, run `wails3 generate bindings` from the repository root. It writes `frontend/bindings/` (git-ignored). `wails3 dev` and `wails3 build` regenerate it too. Do not edit those files by hand.

## Task files

Wails v3 uses [Task](https://taskfile.dev) to orchestrate this project's build, packaging and development workflows (`wails3 task <name>`, or `task` when installed). `wails3 task --list-all` lists every task.

| File | Role |
| --- | --- |
| `Taskfile.yml` | Root. Includes `common` (`build/Taskfile.yml`), `linux` (`build/linux/Taskfile.yml`) and `windows` (`build/windows/Taskfile.yml`). `build`, `package` and `run` dispatch to `{{OS}}:<task>`. Also `dev` and `setup:docker`. |
| `build/Taskfile.yml` | Shared tasks: frontend dependencies and build, bindings generation, `go mod tidy`, Vite dev server, `update:build-assets`, Docker image setup. |
| `build/linux/Taskfile.yml` | Linux build (native or Docker), AppImage, deb, rpm and Arch packages, `.desktop` generation, optional PGP signing. |
| `build/windows/Taskfile.yml` | Windows build (native only), resource generation, NSIS installer. |
| `build/config.yml` | Wails project config: `info` metadata and the `dev_mode` commands. |

Root tasks: `build`, `package`, `run`, `dev` (`wails3 dev -config ./build/config.yml -port` with `VITE_PORT`, default 9245), `setup:docker`.

### Shared tasks (build/Taskfile.yml)

| Task | What it does |
| --- | --- |
| `go:mod:tidy` | `go mod tidy`, run once per invocation. |
| `install:frontend:deps` | Dispatches to `install:frontend:deps:<PACKAGE_MANAGER>` (default `npm`, which runs `npm ci` when `package.json` or `package-lock.json` changes). Also variants for bun, pnpm and yarn. |
| `build:frontend` | Depends on dependency install and `generate:bindings`. Runs `build` (or `build:dev` with `DEV=true`) with `PRODUCTION` set accordingly. Output `frontend/dist`. |
| `generate:bindings` | `wails3 generate bindings -f <BUILD_FLAGS> -clean=true` (adds `-obfuscated` when `OBFUSCATED=true`). Output `frontend/bindings`. |
| `dev:frontend` | Vite dev server on `VITE_PORT`, strict port. Used by `wails3 dev`. |
| `update:build-assets` | Runs `wails3 update build-assets` inside `build/`. This overwrites the customized Linux assets (nfpm, desktop file). Do not run it here. See the warning in `build/config.yml`. |
| `setup:docker` | Builds the `wails-cross` image from `build/docker/Dockerfile.cross`. Needs Docker. |

### Linux (build/linux/Taskfile.yml)

- `APP_ID` is `io.github.gjrud.steam-achievement-tracker`. It must equal `ApplicationID` in `main.go`. GTK sends it as the Wayland app id, and the desktop shell matches the window to the `.desktop` file and the icon with the same name. It is not `config.AppID` (`steam-achievement-tracker`, the keyring service name); the two differ on purpose.
- `build` selects `build:native` or `build:docker`. Native is used when `CGO_ENABLED` is not `1` (the `purego` tag), or when the host is Linux with a C compiler (`wails3 tool has "gcc|clang"`) and the target arch equals the host. Otherwise it uses `build:docker`.
- `build:native` runs `go build` with `GOOS=linux`. Production uses `-tags production -trimpath -buildvcs=false -ldflags="-w -s"`, plus the version stamp `-X ...internal/version.tag=sat-version:<PKG_VERSION>;` when `PKG_VERSION` is set (see [Version stamping and marker checks](#version-stamping-and-marker-checks)). Dev uses `-gcflags=all="-l"` and no stripping. Obfuscated builds use `garble` (v0.18.0 in the error message) and add the `wails_obfuscated` tag.
- `build:docker` runs the `wails-cross` image with the repository mounted. The image must already exist (`setup:docker`).
- `package` runs `create:appimage`, `create:deb`, `create:rpm` and `create:aur`.
  - `create:appimage` runs `wails3 generate appimage` in `build/linux/appimage`, using a 512x512 PNG (`build/appicon-512.png`) named after the app id, and the generated desktop file. It runs with `VERSION` unset, so the menu entry has no version suffix.
  - `generate:deb`, `generate:rpm`, `generate:aur` run `wails3 tool package` with `build/linux/nfpm/nfpm.yaml`, writing into `bin/`.
- `generate:dotdesktop` writes `build/linux/<APP_ID>.desktop` and appends `StartupWMClass=<APP_ID>`. It is git-ignored. Several tasks depend on it, and it is marked `run: once` because the append is not idempotent.
- `run` executes `bin/steam-achievement-tracker`.
- `sign:deb`, `sign:rpm`, `sign:packages` call `wails3 tool sign`. They are optional. `PGP_KEY` is commented out in the vars, so the key comes from the global `wails3 setup` configuration. The passphrase comes from the system keychain. No signing is configured in this repo, and the release does not sign packages.

### Windows (build/windows/Taskfile.yml)

- Native build only. It needs a Windows host with MinGW gcc on `PATH`, because `go-sqlite3` uses CGO. The precondition checks `gcc --version` and `OS == windows`.
- `VERSION_CORE` is the numeric `X.Y.Z` part of `PKG_VERSION`: a leading `v`, pre-release and build metadata are dropped. An empty `PKG_VERSION` gives `0.0.0`, which only dev builds reach. `WIN_VERSION` appends `.0` for the Windows `X.Y.Z.W` resource version. The precondition fails when `VERSION_CORE` is not `X.Y.Z`.
- `build` runs `generate:resources`, then `generate:syso`, then `go build`, with a deferred `cleanup:syso`. The `.syso` (`wails_windows_amd64.syso`) is linked into the package, so it must not remain after the task.
  - Production: `-tags production -trimpath -buildvcs=false -ldflags="-w -s -H windowsgui -X github.com/gjrud/steam-achievement-tracker/internal/version.tag=sat-version:<PKG_VERSION>;"` (GUI subsystem, no console, version stamp). Output `bin/steam-achievement-tracker.exe`.
  - Dev (`DEV=true`): no `windowsgui`, so logs go to a console.
- `generate:resources` renders `info.json` and `wails.exe.manifest` into `.task/resources` (git-ignored) from `build/config.yml` `info`, with `WIN_VERSION` and the description "Steam Achievement Tracker".
- `create:nsis:installer` runs `wails3 generate webview2bootstrapper` (embedded in the CLI, no download), then `makensis` on `build/windows/nsis/project.nsi` with `-DWAILS_INSTALL_SCOPE=user -DREQUEST_EXECUTION_LEVEL=user -DINFO_PRODUCTVERSION=<VERSION_CORE>`. It does not rebuild the exe. Output `bin/steam-achievement-tracker-amd64-installer.exe`.
- `package` runs `build` then `create:nsis:installer`.

### NSIS files (build/windows/nsis)

- `project.nsi`: the installer script. In user scope, it installs to `%LOCALAPPDATA%\Programs\Steam Achievement Tracker` with no UAC prompt. There is no directory page in user scope. It creates a Start Menu shortcut only. The uninstaller removes the exe, the `<exe>.old.*` and `<exe>.bak` files that in-app updates leave (see [platform.md](platform.md#updater-leftovers)), `uninstall.exe`, the empty install directory and the shortcut. It deliberately keeps data, cache and the API key (see the comments in the file and [platform.md](platform.md)). Do not add `RMDir /r` on `$INSTDIR` or on the data and cache paths.
  The installer writes the Apps & Features entry under `UNINST_KEY_NAME` (company plus product name by default). The app checks `DisplayVersion` in that key at each start and writes it when the stored value differs from the running version (see [updater.md](updater.md#after-an-update)). `UNINST_KEY_NAME` must stay in sync with `uninstallKeyPath` in `internal/update/postupdate_windows.go`; change both together.
- `VIProductVersion` is `INFO_PRODUCTVERSION.0`.
- WebView2: `project.nsi` calls its own `app.webview2runtime` macro instead of the stock `wails.webview2runtime`; `WAILS_SKIP_WEBVIEW2` makes the stock one a no-op if it is ever inserted again. The macro does nothing if the runtime key already exists. Otherwise it runs the bootstrapper, polls the runtime registry key for up to 120 s (the bootstrapper can exit before the update service finishes), and shows a warning if the key never appears or the bootstrapper cannot start. The install continues after the warning. The macro is local because `wails_tools.nsh` is regenerated.
- `wails_tools.nsh`: the helper macros the script includes. The file header says it is generated. This repo edits it only by regenerating with the command in `build/config.yml`, copying the `windows/nsis/` output, and never through `update:build-assets` on `build/`.
- The WebView2 bootstrapper is written into this directory at package time (by `wails3 generate webview2bootstrapper`). It is git-ignored by the `*.exe` rule.
- `build/windows/icon.ico` is used for the exe resource and the installer icons.

## Configuration (build/config.yml)

- `info`: `companyName`, `productName`, `productIdentifier` (`io.github.gjrud.steam-achievement-tracker`), `description`, `copyright`, `comments`. Windows resources use these values (through `generate:resources` and `wails_tools.nsh`). Linux packages use `nfpm.yaml`.
- `dev_mode`: on `wails3 dev`, runs `wails3 build DEV=true` (blocking), then `wails3 task common:dev:frontend` (background, waits for the Vite URL), then `wails3 task run` (primary, shuts everything down on exit). File watching ignores `frontend/`, `node_modules/`, `bin/`, `.git`, and `*_test.go`.

The header of `build/config.yml` warns against running `common:update:build-assets` in this repository.

## Linux packaging inputs (build/linux)

### nfpm (build/linux/nfpm/nfpm.yaml)

- `name`, `version: ${VERSION}`, `arch: ${GOARCH}`, `release: "1"`, `section: games`, maintainer from `GIT_COMMITTER_NAME` and `GIT_COMMITTER_EMAIL`, and license MIT.
- Contents:
  - `./bin/steam-achievement-tracker` -> `/usr/local/bin/steam-achievement-tracker`
  - `./build/appicon.svg` -> hicolor scalable `io.github.gjrud.steam-achievement-tracker.svg`
  - `./build/appicon-512.png` -> hicolor `512x512` `...png` (the 512 size is the largest hicolor directory, so a larger PNG would be ignored)
  - `./build/linux/io.github.gjrud.steam-achievement-tracker.desktop` -> `/usr/share/applications/`
- Dependencies: `libgtk-4-1`, `libwebkitgtk-6.0-4` (Debian names). `overrides` give the RPM names (`gtk4`, `webkitgtk6.0`) and the Arch names (`gtk4`, `webkitgtk-6.0`).
- `scripts.postinstall`: `build/linux/nfpm/scripts/postinstall.sh`. It refreshes the desktop database, the hicolor icon cache, and the MIME database. The MIME step refers to `x-scheme-handler` custom URL schemes, but the app registers none (no `Protocols` in `main.go`).
- There is no pre-remove or post-remove script. Removing a package deletes only the installed files. The app's data, cache, state folders and the keyring item stay, which matches the README.

### AppImage (build/linux/appimage)

`create:appimage` uses `wails3 generate appimage`, not the script in this folder. The directory also holds `build.sh`, a `linuxdeploy` script that no task calls. The `.gitignore` entries for `linuxdeploy*` and `*.AppDir/` match what that script produces.

The Wails CLI names the AppImage `steam-achievement-tracker-x86_64.AppImage` in `bin/` (for amd64). The release job renames it to `steam-achievement-tracker-linux-x86_64.AppImage`, the name the in-app updater matches (`internal/update/match.go`). Package files keep their names. The rename happens only in the release, so a local AppImage has the CLI name.

An AppImage cannot install the desktop entry and icons that the window uses. AppImageLauncher renames the entry, so on KDE the taskbar and menu icons are right. The window has no native title bar, so there is no title-bar icon to get wrong; the app's own title bar (drawn in the frontend) always shows its built-in icon. The packages install the entry and icons directly.

## Docker cross-compile image (build/docker/Dockerfile.cross)

Used by `setup:docker` and `build:docker` (Linux builds on non-Linux hosts, without a C compiler, or for another architecture). It is built once with `wails3 task setup:docker` (about 800 MB).

- Base `golang:1.27-trixie`. Installs GTK 4 and WebKitGTK 6.0 development packages, GTK 3 and webkit2gtk-4.1 development packages (both checked with pkg-config), Node.js and npm (`build.sh` runs `npm` when `frontend/dist` is missing), and garble (`GARBLE_VERSION=v0.18.0`). Zig 0.14.0 is downloaded and its SHA-256 checked against Zig's index. A macOS SDK is downloaded for Darwin targets.
- `ENTRYPOINT /usr/local/bin/build.sh <os> <arch>` builds `bin/<app>-<os>-<arch>`, with `-tags production` (plus `EXTRA_TAGS`, and `wails_obfuscated` when `OBFUSCATED=true`).
- Only the Linux targets are reachable from the Taskfiles. The Darwin and Windows wrappers are present in the image but no task calls them. The Windows task states that it does not cross-compile.

## CI (.github/workflows/ci.yml)

- Triggers: `pull_request`, `push` to `main`, and `workflow_call` (lets `release.yml` reuse CI). Concurrency cancels older runs of the same ref.
- `WAILS_VERSION: v3.1.0`. It must match `go.mod` (see Upgrading Wails).
- Job `test-matrix` (`ubuntu-latest`, `windows-latest`, `fail-fast: false`, 30 min timeout):
  - Both legs: checkout (`persist-credentials: false`), `setup-go` from `go.mod` with the default cache.
  - Linux: Node 22 with npm cache; apt installs `build-essential pkg-config libgtk-4-dev libwebkitgtk-6.0-dev`; `npm ci` in `frontend/`; installs the Wails CLI; writes a placeholder `frontend/dist/.gitkeep` and runs `wails3 generate bindings`; `npm run build`; `npm test`; `go vet ./...`; `go test -race ./...`.
  - Windows: a placeholder `frontend/dist`; checks `gcc`; runs `go vet ./...`, `go test -race ./...`, and the production exe smoke build with `CGO_ENABLED=1`. The build uses `go build -tags production -ldflags "-w -s -H windowsgui"` into a temp dir. It has no version stamp, so it has no updater. No npm, Wails CLI or GTK on Windows.
- Job `test` (`needs: test-matrix`, `if: always()`, 5 min): passes only when the matrix succeeded. This is the required status check. Branch protection requires `test`, not the matrix legs.

## Cutting a release

`.github/workflows/release.yml` builds releases when a version tag is pushed. The tag is the only place the version is written.

Pushing a version tag triggers the release workflow, which can publish a GitHub release. Get explicit authorization before pushing a release or `-rc` tag.

1. Merge the work into `main`. The repository allows only squash merges.
2. Tag the tip of `main` and push the tag:

   ```sh
   git switch main && git pull
   git tag -a v1.2.3 -m "v1.2.3"
   git push origin v1.2.3
   ```

The tag must look like `vMAJOR.MINOR.PATCH`, optionally with a suffix (`v1.2.3-rc.1`). A suffix marks the release as a pre-release. The workflow then:

1. Checks that the tagged commit is an ancestor of `origin/main`; otherwise nothing is published. Because only squash merges are allowed, a tag on a PR-branch commit can never pass: tag the squashed commit on `main` instead. Tags are immutable, so after a rejected tag either use the next version or follow the bad-tag procedure below.
2. Runs the same checks as `ci.yml`.
3. Runs `wails3 package` on Ubuntu 24.04, with the tag without its `v` as `VERSION`. It checks that the AppImage, deb, rpm and Arch packages exist, that the deb version matches the tag, and that the build did not change `go.mod` or `go.sum`. It also checks the version marker (`sat-version:<version>;`) in the freshly built `bin/steam-achievement-tracker` (see [Version stamping and marker checks](#version-stamping-and-marker-checks)).
4. Runs the Windows job on `windows-latest` after the tests pass: `wails3 task windows:build`, then the NSIS installer from the same exe. It checks the exe's version marker before the installer is built, that the exe's ProductVersion matches the tag's numeric core (`1.2.3-rc.1` gives `1.2.3.0`), that the build did not change `go.mod` or `go.sum`, and that exactly the portable exe and the installer exist. The files are renamed to `steam-achievement-tracker-windows-amd64-portable.exe` (no version, on purpose; see [Updater asset names](#updater-asset-names)) and `steam-achievement-tracker-<version>-windows-amd64-setup.exe`.
5. Publishes a GitHub release with the four Linux packages (the AppImage under its updater name, see below), the two Windows executables, a source archive, `SHA256SUMS`, build provenance attestations, and a changelog of every non-merge commit since the previous release, grouped by conventional-commit type (see [Changelog](#changelog-githubscriptschangelogsh)). A final release lists changes since the previous final release; pre-release tags are skipped.

Windows executables are deliberately unsigned: SmartScreen warnings remain until a build earns reputation. Do not add signing steps. Validate local Windows build and installer tasks on a Windows host. Hosted release-workflow validation requires an authorized `-rc` tag before a final release.

Tags matching `v*` are protected by repository rulesets: only admins can create them, and they cannot be moved or deleted. To fix a bad tag, disable the *Release tags: immutable* ruleset in the repository settings, delete the release if one was published (`gh release delete vX.Y.Z --yes`) and the tag, re-push the tag, then re-enable the ruleset. Pushing a tag that already has a release fails at the publish step. With *Immutable releases* enabled (see [Repository settings](#repository-settings-manual)), a tag name can never be reused once a release was published for it, even after the release and tag are deleted. So the delete-and-re-push procedure works only if no release was published for the tag (the workflow failed before the *Publish release* step). Once a release is published, use the next version instead. Pushing more than three tags at once triggers no workflow runs. The Arch package of a pre-release gets the same version as the final release (nfpm drops the suffix there), so do not rely on pacman upgrading from a pre-release.

### Repository settings (manual)

These settings live in GitHub, not in the repository, so no file enforces them. Check them when they change.

- Tag rulesets *Release tags: admin-only creation* and *Release tags: immutable* are active. They cover `v*` tags (see the paragraph above).
- *Immutable releases* is off. A repository admin should enable it (Settings, General, Releases). It locks a release's assets after publication. Tag immutability does not cover files, so this setting is the only one that protects published assets. Once it is on, a published release's tag name is burned even after deletion; see the bad-tag procedure in [Cutting a release](#cutting-a-release) before deleting a release.
- The `release` job has no `environment:`. An environment adds an approval gate only when it has required reviewers. Without them it does nothing, so none is set. Add one only together with required reviewers.

## Release (.github/workflows/release.yml)

Job-by-job reference for the release workflow. Trigger: a push of a tag matching `v[0-9]+.[0-9]+.[0-9]+*`. The `verify` job then checks that the tag is semver `vMAJOR.MINOR.PATCH[-prerelease]` with no leading zeros (same rule as `internal/version.Valid`). Concurrency does not cancel (`cancel-in-progress: false`).

| Job | Runs on | Steps |
| --- | --- | --- |
| `verify` | ubuntu-24.04 | Tag is semver `vMAJOR.MINOR.PATCH[-prerelease]` with no leading zeros (same rule as `internal/version.Valid`). The tagged commit is an ancestor of `origin/main`, otherwise the job fails. |
| `test` | (reusable) | `uses: ./.github/workflows/ci.yml`. The same checks as CI. |
| `package` | ubuntu-24.04 | Go, Node 22, apt packages, Wails CLI. `VERSION` from the tag without `v`. `wails3 package`. Checks the version marker in `bin/steam-achievement-tracker` (`grep -aqF`). Checks that `go.mod` and `go.sum` are unchanged. Checks that exactly one AppImage, deb, rpm and `pkg.tar.zst` exist. Checks that `dpkg-deb` reports the tag version (the first `-` becomes `~`, then release `-1` is appended). Uploads `linux-packages`. |
| `package-windows` | windows-latest | Go, Node 22 (no caches, see below), a placeholder `frontend/dist`, the Wails CLI (its `GOPATH/bin` is added to `PATH`), gcc and makensis check (Chocolatey NSIS 3.13.0 as fallback). `VERSION` from the tag. `wails3 task windows:build`, then the version marker check on `bin/steam-achievement-tracker.exe`, then `windows:create:nsis:installer`. Checks `go.mod`/`go.sum`. Checks that exactly two exes exist. Checks that the exe `ProductVersion` equals the numeric core plus `.0`. Renames to `steam-achievement-tracker-windows-amd64-portable.exe` (unversioned on purpose) and `steam-achievement-tracker-<version>-windows-amd64-setup.exe`. Uploads `windows-packages`. |
| `release` | ubuntu-24.04 | Full-history checkout. Downloads both artifacts into one flat `dist/` (fails on subdirectories or a missing portable/setup exe). Renames the AppImage in `dist/` to `steam-achievement-tracker-linux-x86_64.AppImage` (it fails unless exactly one AppImage is there). Creates the source archive (`git archive`, tar.gz). Writes `SHA256SUMS` over all of `dist/` (`sha256sum -- *`) and fails unless it lists the portable exe and the renamed AppImage by name. Generates notes with `changelog.sh`. Attests build provenance (`actions/attest-build-provenance`). Creates the GitHub release with `gh release create --verify-tag`, adding `--prerelease` when the tag has a `-` suffix. |

Permissions: `contents: write`, `id-token: write`, `attestations: write` on `release` only.

Artifacts: AppImage (`steam-achievement-tracker-linux-x86_64.AppImage`), deb, rpm, `pkg.tar.zst`, the portable exe (`steam-achievement-tracker-windows-amd64-portable.exe`, unversioned on purpose), the setup exe (`steam-achievement-tracker-<version>-windows-amd64-setup.exe`), the source tarball and `SHA256SUMS`.

Pins, caches and credentials in both workflows:

- Every action is pinned to a full commit SHA with a `# vX.Y.Z` comment (see [Dependabot](#dependabot-githubdependabotyml)).
- Release builds use no caches. `setup-go` in `package` and `package-windows` sets `cache: false`. `setup-node` in both jobs sets `package-manager-cache: false`, so the no-cache rule does not depend on a `packageManager` field in any `package.json`. An artifact never comes from a cache restored from another run. CI keeps its default caches, including the reusable `test` job that `release.yml` calls (it produces no release artifacts).
- Every `actions/checkout` sets `persist-credentials: false`. `gh release create` gets `GH_TOKEN` from `github.token` through the job `env:`. The other release steps only read the local repository (`git archive`, and `git rev-parse`, `git describe` and `git log` in changelog.sh), so they need no credentials.
- The Chocolatey NSIS fallback (`package-windows`, "Verify toolchain") runs only when `makensis` is missing from `windows-latest`. It is pinned to `nsis --version=3.13.0`. To update the pin, check the current version on community.chocolatey.org/packages/nsis and bump `--version` in `release.yml`. The fallback cannot be exercised locally.

### Changelog (.github/scripts/changelog.sh)

`changelog.sh <tag>` prints Markdown notes for every non-merge commit since the previous release.

- Previous release: the nearest earlier `v*` tag (`git describe --match 'v[0-9]*'`). For a final release, pre-release tags (containing `-`) are excluded. Pre-releases compare against the previous tag of any kind.
- Commits are grouped by conventional-commit type, in this order: breaking (a `!` after the type or scope), feat, fix, perf, refactor, docs, test, build, ci, chore, other. Only known types are stripped from the subject; scopes become bold prefixes.
- Ends with a "Full Changelog" link: a compare link from the previous tag to this one, or a `commits/<tag>` link when there is no previous tag.
- Needs full history and tags (`fetch-depth: 0`). Uses `GITHUB_REPOSITORY`, default `gjrud/steam-achievement-tracker`.

## Dependabot (.github/dependabot.yml)

Dependabot schedules weekly updates for three ecosystems. The Go and npm groups cover minor and patch updates only:

- `gomod` at `/` (group `go-minor-patch`)
- `npm` at `/frontend` (group `npm-minor-patch`)
- `github-actions` at `/` (one group for all actions, every update type; `patterns: ["*"]`)

Action pins: every `uses:` in `.github/workflows/` is a full commit SHA followed by a full-version comment, for example `uses: actions/checkout@<sha> # v7.0.1`. Tags and branches are not used, because they can move. The full-version comment is what lets Dependabot keep it current: Dependabot rewrites the SHA and the `# vX.Y.Z` comment together. Review that PR like any other update. To pin a new version by hand, resolve the tag with `git ls-remote https://github.com/actions/<name>.git 'refs/tags/<tag>' 'refs/tags/<tag>^{}'` and use the peeled `^{}` commit for an annotated tag. The local reusable workflow `./.github/workflows/ci.yml` has no SHA and is not an action.

## Version handling

- Source of truth: the tag. The release workflow sets `VERSION=${GITHUB_REF_NAME#v}`. Locally, `PKG_VERSION` in the root `Taskfile.yml` is `VERSION` if set, else the `vX.Y.Z` tag on `HEAD` (other tags such as `nightly` are ignored), else empty, and the package tasks fail. The repo does not store a version.
- `nfpm.yaml` expands `${VERSION}`. Run directly through `wails3 tool package` without `VERSION`, packaging still succeeds with a wrong version (a deb shows `0.0.0~rc0-1`). The `PKG_VERSION` guard prevents that through the tasks, and the release job still checks the deb version.
- Windows: `PKG_VERSION` (same rule: `VERSION`, else the tag on `HEAD`, else fail), `VERSION_CORE` and `WIN_VERSION` as above. `1.2.3-rc.1` becomes `1.2.3.0` in the exe and NSIS. Release artifact names keep the full version, except the portable exe and the AppImage. Both are unversioned on purpose, so a self-updated file keeps a stable name (see [Updater asset names](#updater-asset-names)).
- `VERSION` may have or omit the `v` prefix (`1.2.3`, `v1.2.3`, `1.2.3-rc.1`). The tag fallback ignores tags that do not start with `v` and a digit, so `nightly` never sets a version.
- `create:appimage` unsets `VERSION` on purpose.
- The frontend `package.json` `version` is `0.0.0` and is not the app version.

### Version stamping and marker checks

The app's own version is also compiled into the binary, for the in-app updater (see [updater.md](updater.md#version-stamp)).

- The production branch of the Linux `build:native` task and the Windows `build` task adds `-X github.com/gjrud/steam-achievement-tracker/internal/version.tag=sat-version:<PKG_VERSION>;` to the existing single `-ldflags` string. `PKG_VERSION` has no leading `v`. The Linux flag is added only when `PKG_VERSION` is set. The Windows build already fails its precondition when it is empty. The DEV branches (`DEV=true`) add no flag.
- The marker is the labeled, terminated string `sat-version:X.Y.Z;` (`X.Y.Z` may carry a `-pre` part). At runtime the updater accepts only a version that matches `X.Y.Z[-pre]` and passes `semver.IsValid`. A stamp that does not match leaves the updater unavailable, and the update UI does not show.
- The `-X` path must be the full import path. A wrong path does not fail the build; the stamp is simply missing. The release checks catch that.
- `-trimpath` leaves the `-ldflags` string out of the binary's build info. Without it, Go records that string, and it contains the marker, so the check would pass even when the `-X` path is wrong. With `-trimpath`, `grep -aqF` finds the marker only if the variable was set.
- The release workflow checks the marker with `grep -aqF "sat-version:${VERSION};"` on `bin/steam-achievement-tracker` (`package` job, after `wails3 package`) and on `bin/steam-achievement-tracker.exe` (`package-windows` job, before the installer is built). A missing or different marker fails the release. Releases are not built with `OBFUSCATED=true`, so the marker is never hidden.
- The AppImage, deb, rpm and Arch packages are compressed, so the marker check runs on the pre-package binary instead.
- Builds without a stamp (`go build`, the CI smoke builds, `wails3 build` with no version and no tag, Docker builds) have no updater. The CI Windows smoke build is a direct `go build`, so it has no stamp.

To test the updater on a local build, use the recipe in [updater.md](updater.md#local-test-build). Do not commit any change made for a local test.

### Updater asset names

The in-app updater matches release assets by name (`internal/update/match.go`), and the release must keep these names:

- The Windows updater takes the asset whose name ends with `-windows-amd64-portable.exe`. Its `SHA256SUMS` line must exist, or the release is notify-only. The release names the portable exe without a version (`steam-achievement-tracker-windows-amd64-portable.exe`) on purpose, so a self-updated file keeps a stable name. Do not add a version back, and do not add a second portable asset.
- The Linux updater takes exactly `steam-achievement-tracker-linux-x86_64.AppImage`. The release job renames the AppImage to this name before it writes `SHA256SUMS`.
- `SHA256SUMS` must list both assets by exact name. The release job checks this (step "Check SHA256SUMS lists the updater assets") and fails otherwise. Without a checksum for the asset, the gate refuses an in-app install.
- Do not rename these assets, change the portable exe pattern, add a version to the portable exe name, or add a second AppImage or portable exe. Any of those makes the updater notify-only or unable to find the asset.
- Pre-release tags (`-` in the version) are marked as pre-releases and are never offered by the updater.

## Generated and ignored files

These paths are git-ignored, as documented in `.gitignore` and above:

| Path | Produced by |
| --- | --- |
| `frontend/bindings/` | `generate:bindings` |
| `frontend/dist/` | `build:frontend`; placeholder in CI |
| `frontend/node_modules/`, `node_modules/` | `npm ci` |
| `bin/`, `build/bin/` | build and package tasks |
| `.task/`, `.wails/` | `generate:resources`, Wails CLI |
| `*.syso` | `generate:syso` (removed by `cleanup:syso`) |
| `*.exe` | build output and the WebView2 bootstrapper in `build/windows/nsis/` |
| `build/linux/*.desktop`, `build/linux/appimage/*.png`, `build/linux/appimage/steam-achievement-tracker`, `build/linux/appimage/build/`, `build/linux/appimage/*.AppDir/`, `build/linux/appimage/*.AppImage`, `build/linux/appimage/linuxdeploy*` | Linux packaging |
| `/steam-achievement-tracker` | Binary from a plain `go build` in the repo root (the rule is anchored to the root) |
| `tmp/` | local scratch (also the gremlins output folder in [testing.md](testing.md)) |

Committed generated files: `build/windows/nsis/wails_tools.nsh` (generated by Wails; its header says so, and the NSIS section describes how it is updated). `frontend/package-lock.json` is generated by npm and is used by `npm ci`.

Do not hand-edit anything in `frontend/bindings/`.

## Upgrading Wails

The Wails version is specified in these places. Update them together:

1. `go.mod`: `github.com/wailsapp/wails/v3`. Run `go mod tidy`; `go.sum` changes too.
2. `.github/workflows/ci.yml`: `WAILS_VERSION` (the CLI is installed with `go install .../wails3@$WAILS_VERSION`).
3. `.github/workflows/release.yml`: `WAILS_VERSION`.
4. `frontend/package.json`: `@wailsio/runtime`, pinned exactly (no `^`). Update the matching entry in `frontend/package-lock.json` (`npm install --save-exact @wailsio/runtime@<version>` in `frontend/`).
5. Locally, install the same CLI: `go install github.com/wailsapp/wails/v3/cmd/wails3@<version>`.
6. Regenerate bindings with `wails3 generate bindings`. Update `frontend/src/test/wails-app-mock.js` if the exported methods changed. The drift test enforces it.
7. If the NSIS helper or its templates changed, regenerate `wails_tools.nsh` as described in the NSIS section, and do not run `update:build-assets` on `build/`.
8. Dockerfile.cross does not pin the Wails version (it builds the module from the repo). Only the Go image tag is pinned there (`golang:1.27-trixie`). Check that it still matches `go.mod`'s Go version (`go 1.27.4`).
9. Re-check the custom scrollbar's resize insets (`ScrollBar.svelte`, `.app-scrollbar` in `app.css`; `--resize-inset-right`, 6 px: just over the 5 px right strip; `--resize-inset-bottom`, 15 px: the corner square size; both in `frontend/src/style.css`; the thumb sits that far from the right and bottom edges, and the track starts at `--title-bar-height` plus `--content-top-gap`) against Wails' resize zones in `frontend/node_modules/@wailsio/runtime/dist/drag.js` (5 px along each edge, corners 5 px plus `resizeCornerExtra`). The zone widths come from `GetFlag("system.resizeHandleWidth")` and `GetFlag("system.resizeHandleHeight")`, but on Windows the Go side writes them as a nested `system` map (`application_windows.go:83-86` in v3.1.0), so the flat lookup misses and the 5 px default applies whatever the system metric says. If a later Wails fixes that, the zone can change width: update the insets, then check the thumb on Windows as in [testing.md](testing.md#title-bar-manual-checks).

Workflow or `changelog.sh` changes affect releases. For hosted validation, follow the authorization and `-rc` guidance in [Cutting a release](#cutting-a-release).

