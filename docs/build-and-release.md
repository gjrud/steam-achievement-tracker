# Build, packaging and release

How the app is built, packaged and released. The user-facing install and release steps are in
the README (`Build & run`, `Releasing`). This file describes the files behind them.
Testing commands are in [testing.md](testing.md); frontend build details are in
[frontend.md](frontend.md).

## Task files

Wails v3 runs everything through [Task](https://taskfile.dev) (`wails3 task <name>`, or `task`
when installed). `wails3 task --list-all` lists every task.

| File | Role |
| --- | --- |
| `Taskfile.yml` | Root. Includes `common` (`build/Taskfile.yml`), `linux` (`build/linux/Taskfile.yml`) and `windows` (`build/windows/Taskfile.yml`). `build`, `package` and `run` dispatch to `{{OS}}:<task>`. Also `dev` and `setup:docker`. |
| `build/Taskfile.yml` | Shared tasks: frontend dependencies and build, bindings generation, `go mod tidy`, Vite dev server, `update:build-assets`, Docker image setup. |
| `build/linux/Taskfile.yml` | Linux build (native or Docker), AppImage, deb, rpm and Arch packages, `.desktop` generation, optional PGP signing. |
| `build/windows/Taskfile.yml` | Windows build (native only), resource generation, NSIS installer. |
| `build/config.yml` | Wails project config: `info` metadata and the `dev_mode` commands. |

Root tasks: `build`, `package`, `run`, `dev` (`wails3 dev -config ./build/config.yml -port`
with `VITE_PORT`, default 9245), `setup:docker`.

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

- `APP_ID` is `io.github.gjrud.steam-achievement-tracker`. It must equal `ApplicationID` in
  `main.go`. GTK sends it as the Wayland app id, and the desktop shell matches the window to the
  `.desktop` file and the icon with the same name.
- `build` chooses `build:native` or `build:docker`. Native is used when `CGO_ENABLED` is not `1`
  (the `purego` tag), or when the host is Linux with a C compiler (`wails3 tool has "gcc|clang"`)
  and the target arch equals the host. Otherwise it uses `build:docker`.
- `build:native` runs `go build` with `GOOS=linux`. Production uses `-tags production -trimpath
  -buildvcs=false -ldflags="-w -s"`. Dev uses `-gcflags=all="-l"` and no stripping. Obfuscated
  builds use `garble` (v0.16.0 in the error message) and add the `wails_obfuscated` tag.
- `build:docker` runs the `wails-cross` image with the repo mounted. It requires that image to
  exist (`setup:docker`).
- `package` runs `create:appimage`, `create:deb`, `create:rpm` and `create:aur`.
  - `create:appimage` runs `wails3 generate appimage` in `build/linux/appimage`, using a 512x512
    PNG (`build/appicon-512.png`) named after the app id, and the generated desktop file. It runs
    with `VERSION` unset, so the menu entry has no version suffix.
  - `generate:deb`, `generate:rpm`, `generate:aur` run `wails3 tool package` with
    `build/linux/nfpm/nfpm.yaml`, writing into `bin/`.
- `generate:dotdesktop` writes `build/linux/<APP_ID>.desktop` and appends `StartupWMClass=<APP_ID>`.
  It is git-ignored. Several tasks depend on it, and it is marked `run: once` because the append
  is not idempotent.
- `run` executes `bin/steam-achievement-tracker`.
- `sign:deb`, `sign:rpm`, `sign:packages` call `wails3 tool sign`. They are optional. `PGP_KEY`
  is commented out in the vars, so the key comes from the global `wails3 setup` configuration.
  The passphrase comes from the system keychain. No signing is configured in this repo, and the
  release does not sign packages.

### Windows (build/windows/Taskfile.yml)

- Native build only. It needs a Windows host with MinGW gcc on `PATH`, because `go-sqlite3` uses
  CGO. The precondition checks `gcc --version` and `OS == windows`.
- `VERSION` is the only version source. `VERSION_CORE` is the numeric `X.Y.Z` part: a leading
  `v`, pre-release and build metadata are dropped. Unset falls back to `0.0.0`.
  `WIN_VERSION` appends `.0` for the Windows `X.Y.Z.W` resource version. The precondition fails
  when `VERSION_CORE` is not `X.Y.Z`.
- `build` runs `generate:resources`, then `generate:syso`, then `go build`, with a deferred
  `cleanup:syso`. The `.syso` (`wails_windows_amd64.syso`) is linked into the package, so it
  must not remain after the task.
  - Production: `-tags production -trimpath -buildvcs=false -ldflags="-w -s -H windowsgui"`
    (GUI subsystem, no console). Output `bin/steam-achievement-tracker.exe`.
  - Dev (`DEV=true`): no `windowsgui`, so logs go to a console.
- `generate:resources` renders `info.json` and `wails.exe.manifest` into `.task/resources`
  (git-ignored) from `build/config.yml` `info`, with `WIN_VERSION` and the description
  "Steam Achievement Tracker".
- `create:nsis:installer` runs `wails3 generate webview2bootstrapper` (embedded in the CLI, no
  download), then `makensis` on `build/windows/nsis/project.nsi` with
  `-DWAILS_INSTALL_SCOPE=user -DREQUEST_EXECUTION_LEVEL=user -DINFO_PRODUCTVERSION=<VERSION_CORE>`.
  It does not rebuild the exe. Output `bin/steam-achievement-tracker-amd64-installer.exe`.
- `package` runs `build` then `create:nsis:installer`.

### NSIS files (build/windows/nsis)

- `project.nsi`: the installer script. User scope: installs to
  `%LOCALAPPDATA%\Programs\Steam Achievement Tracker` with no UAC prompt. There is no directory
  page in user scope. It creates a Start Menu shortcut only. The uninstaller removes the exe,
  `uninstall.exe`, the empty install directory and the shortcut. It deliberately keeps data, cache
  and the API key (see the comments in the file and [platform.md](platform.md)). Do not add
  `RMDir /r` on `$INSTDIR` or on the data and cache paths.
- `VIProductVersion` is `INFO_PRODUCTVERSION.0`.
- `wails_tools.nsh`: the helper macros the script includes. The file header says it is generated.
  This repo edits it only by regenerating with the command in `build/config.yml`, copying the
  `windows/nsis/` output, and never through `update:build-assets` on `build/`.
- The WebView2 bootstrapper is written into this directory at package time (by
  `wails3 generate webview2bootstrapper`). It is git-ignored by the `*.exe` rule.
- `build/windows/icon.ico` is used for the exe resource and the installer icons.

## Configuration (build/config.yml)

- `info`: `companyName`, `productName`, `productIdentifier` (`io.github.gjrud.steam-achievement-tracker`),
  `description`, `copyright`, `comments`. Windows resources use these values (through
  `generate:resources` and `wails_tools.nsh`). Linux packages use `nfpm.yaml`.
- `dev_mode`: on `wails3 dev`, runs `wails3 build DEV=true` (blocking), then
  `wails3 task common:dev:frontend` (background, waits for the Vite URL), then `wails3 task run`
  (primary, shuts everything down on exit). File watching ignores `frontend/`, `node_modules/`,
  `bin/`, `.git`, and `*_test.go`.

The header of `build/config.yml` warns not to run `common:update:build-assets` in this repo.

## Linux packaging inputs (build/linux)

### nfpm (build/linux/nfpm/nfpm.yaml)

- `name`, `version: ${VERSION}`, `arch: ${GOARCH}`, `release: "1"`, `section: games`,
  maintainer from `GIT_COMMITTER_NAME` and `GIT_COMMITTER_EMAIL`, and license MIT.
- Contents:
  - `./bin/steam-achievement-tracker` -> `/usr/local/bin/steam-achievement-tracker`
  - `./build/appicon.svg` -> hicolor scalable `io.github.gjrud.steam-achievement-tracker.svg`
  - `./build/appicon-512.png` -> hicolor `512x512` `...png` (the 512 size is the largest hicolor
    directory, so a larger PNG would be ignored)
  - `./build/linux/io.github.gjrud.steam-achievement-tracker.desktop` -> `/usr/share/applications/`
- Dependencies: `libgtk-4-1`, `libwebkitgtk-6.0-4` (Debian names). `overrides` give the RPM
  names (`gtk4`, `webkitgtk6.0`) and the Arch names (`gtk4`, `webkitgtk-6.0`).
- `scripts.postinstall`: `build/linux/nfpm/scripts/postinstall.sh`. It refreshes the desktop
  database, the hicolor icon cache, and the MIME database. The MIME step refers to
  `x-scheme-handler` custom URL schemes, but the app registers none (see Surprises below).

### AppImage (build/linux/appimage)

`create:appimage` uses `wails3 generate appimage`, not the script in this folder. The directory
also holds `build.sh`, a `linuxdeploy` script that no task calls. The `.gitignore` entries for
`linuxdeploy*` and `*.AppDir/` match what that script produces. See Surprises below.

## Docker cross-compile image (build/docker/Dockerfile.cross)

Used by `setup:docker` and by `build:docker` (Linux builds on non-Linux hosts, without a C
compiler, or for another architecture). It is built once with
`wails3 task setup:docker` (about 800 MB).

- Base `golang:1.26-trixie`. Installs GTK 4 and WebKitGTK 6.0 development packages, GTK 3 and
  webkit2gtk-4.1 development packages (both checked with pkg-config), Node.js and npm (`build.sh`
  runs `npm` when `frontend/dist` is missing), and garble (`GARBLE_VERSION=v0.16.0`). Zig 0.14.0 is downloaded and its SHA-256 checked against Zig's
  index. A macOS SDK is downloaded for Darwin targets.
- `ENTRYPOINT /usr/local/bin/build.sh <os> <arch>` builds `bin/<app>-<os>-<arch>`, with
  `-tags production` (plus `EXTRA_TAGS`, and `wails_obfuscated` when `OBFUSCATED=true`).
- Only the Linux targets are reachable from the Taskfiles. The Darwin and Windows wrappers are
  present in the image but no task calls them. The Windows task states that it does not
  cross-compile.

## CI (.github/workflows/ci.yml)

- Triggers: `pull_request`, `push` to `main`, and `workflow_call` (used by `release.yml`).
  Concurrency cancels older runs of the same ref.
- `WAILS_VERSION: v3.1.0`. It must match `go.mod` (see Upgrading Wails).
- Job `test-matrix` (`ubuntu-latest`, `windows-latest`, `fail-fast: false`, 30 min timeout):
  - Both legs: checkout, `setup-go` from `go.mod`.
  - Linux: Node 22 with npm cache; apt installs `build-essential pkg-config libgtk-4-dev
    libwebkitgtk-6.0-dev`; `npm ci` in `frontend/`; installs the Wails CLI; writes a placeholder
    `frontend/dist/.gitkeep` and runs `wails3 generate bindings`; `npm run build`; `npm test`;
    `go vet ./...`; `go test -race ./...`.
  - Windows: a placeholder `frontend/dist`; checks `gcc`; `go vet ./...` and
    `go test -race ./...` with `CGO_ENABLED=1`; smoke-builds the production exe with
    `go build -tags production -ldflags "-w -s -H windowsgui"` into a temp dir. No npm, Wails CLI or
    GTK on Windows.
- Job `test` (`needs: test-matrix`, `if: always()`, 5 min): passes only when the matrix succeeded.
  This is the required status check. Branch protection requires `test`, not the matrix legs.

## Release (.github/workflows/release.yml)

Trigger: a push of a tag matching `v[0-9]+.[0-9]+.[0-9]+*`. The `verify` job then checks the tag
against `vMAJOR.MINOR.PATCH[-prerelease]`. Concurrency does not cancel (`cancel-in-progress: false`).

| Job | Runs on | Steps |
| --- | --- | --- |
| `verify` | ubuntu-24.04 | Tag matches the version pattern. The tagged commit is an ancestor of `origin/main`, otherwise the job fails. |
| `test` | (reusable) | `uses: ./.github/workflows/ci.yml`. The same checks as CI. |
| `package` | ubuntu-24.04 | Go, Node 22, apt packages, Wails CLI. `VERSION` from the tag without `v`. `wails3 package`. Checks that `go.mod` and `go.sum` are unchanged. Checks that exactly one AppImage, deb, rpm and `pkg.tar.zst` exist. Checks that `dpkg-deb` reports the tag version (the first `-` becomes `~`, then release `-1` is appended). Uploads `linux-packages`. |
| `package-windows` | windows-latest | Go, Node 22, a placeholder `frontend/dist`, the Wails CLI (its `GOPATH/bin` is added to `PATH`), gcc and makensis check (Chocolatey NSIS as fallback). `VERSION` from the tag. `wails3 task windows:build`, then `windows:create:nsis:installer`. Checks `go.mod`/`go.sum`. Checks that exactly two exes exist. Checks that the exe `ProductVersion` equals the numeric core plus `.0`. Renames to `steam-achievement-tracker-<version>-windows-amd64-portable.exe` and `...-setup.exe`. Uploads `windows-packages`. |
| `release` | ubuntu-24.04 | Full-history checkout. Downloads both artifacts into one flat `dist/`. Creates the source archive (`git archive`, tar.gz) and `SHA256SUMS`. Generates notes with `changelog.sh`. Attests build provenance (`actions/attest-build-provenance`). Creates the GitHub release with `gh release create --verify-tag`, adding `--prerelease` when the tag has a `-` suffix. |

Permissions: `contents: write`, `id-token: write`, `attestations: write` on `release` only.

Artifacts: AppImage, deb, rpm, `pkg.tar.zst`, the portable exe, the setup exe, the source tarball
and `SHA256SUMS`.

### Changelog (.github/scripts/changelog.sh)

`changelog.sh <tag>` prints Markdown notes for every non-merge commit since the previous release.

- Previous release: the nearest earlier `v*` tag (`git describe --match 'v[0-9]*'`). For a final
  release, pre-release tags (containing `-`) are excluded. Pre-releases compare against the previous
  tag of any kind.
- Commits are grouped by conventional-commit type, in this order: breaking (a `!` after the type
  or scope), feat, fix, perf, refactor, docs, test, build, ci, chore, other. Only known
  types are stripped from the subject; scopes become bold prefixes.
- Ends with a "Full Changelog" link: a compare link from the previous tag to this one, or a
  `commits/<tag>` link when there is no previous tag.
- Needs full history and tags (`fetch-depth: 0`). Uses `GITHUB_REPOSITORY`, default
  `gjrud/steam-achievement-tracker`.

## Dependabot (.github/dependabot.yml)

Weekly updates for three ecosystems. The Go and npm groups cover minor and patch updates only:

- `gomod` at `/` (group `go-minor-patch`)
- `npm` at `/frontend` (group `npm-minor-patch`)
- `github-actions` at `/` (one group for all actions, every update type; `patterns: ["*"]`)

## Version handling

- Source of truth: the tag. The release workflow sets `VERSION=${GITHUB_REF_NAME#v}`. Locally,
  set `VERSION` yourself (`VERSION=0.0.0-dev wails3 package`). The repo does not store a version.
- `nfpm.yaml` expands `${VERSION}`. Without it, packaging still succeeds with a wrong version
  (the README records the resulting deb version as `0.0.0~rc0-1`), and no step fails. That silence is why the
  release job checks the deb version.
- Windows: `VERSION_CORE` and `WIN_VERSION` as above. `1.2.3-rc.1` becomes `1.2.3.0` in the exe and
  NSIS. Release artifact names keep the full version.
- `create:appimage` unsets `VERSION` on purpose.
- The frontend `package.json` `version` is `0.0.0` and is not the app version.

## Generated and ignored files

Git-ignored (from `.gitignore` and the paths above):

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

Committed generated files: `build/windows/nsis/wails_tools.nsh` (generated by Wails; its header says
so, and the NSIS section describes how it is updated). `frontend/package-lock.json` is generated by
npm and is used by `npm ci`.

Do not hand-edit anything in `frontend/bindings/`.

## Upgrading Wails

The Wails version appears in these places. Change all of them together:

1. `go.mod`: `github.com/wailsapp/wails/v3`. Run `go mod tidy`; `go.sum` changes too.
2. `.github/workflows/ci.yml`: `WAILS_VERSION` (the CLI is installed with `go install .../wails3@$WAILS_VERSION`).
3. `.github/workflows/release.yml`: `WAILS_VERSION`.
4. `frontend/package.json`: `@wailsio/runtime`, pinned exactly (no `^`). Update the matching entry in
   `frontend/package-lock.json` (`npm install --save-exact @wailsio/runtime@<version>` in `frontend/`).
5. Locally, install the same CLI: `go install github.com/wailsapp/wails/v3/cmd/wails3@<version>`.
6. Regenerate bindings with `wails3 generate bindings`. Update
   `frontend/src/test/wails-app-mock.js` if the exported methods changed. The drift test enforces it.
7. If the NSIS helper or its templates changed, regenerate `wails_tools.nsh` as described in the
   NSIS section, and do not run `update:build-assets` on `build/`.
8. Dockerfile.cross does not pin the Wails version (it builds the module from the repo). Only the
   Go image tag is pinned there (`golang:1.26-trixie`). Check that it still matches `go.mod`'s Go
   version (`go 1.26.2`).

Changing a workflow or `changelog.sh` is a release change. Test it with a `-rc` pre-release tag
before a final tag, because `v*` tags are immutable under the repository ruleset (README,
`Releasing`).

## Surprises and inconsistencies

- `build/linux/appimage/build.sh` is not used by any task. `create:appimage` uses
  `wails3 generate appimage`. Its `linuxdeploy` download and `.gitignore` entries are leftovers
  from that script.
- `postinstall.sh` refreshes the MIME database for `x-scheme-handler` custom URL schemes. The app
  registers no protocols (no `Protocols` in `main.go`), so that step has no effect.
- The Dockerfile's Darwin and Windows wrappers are not reachable from any task. Only Linux is.
- The Linux `APP_ID` (`io.github.gjrud.steam-achievement-tracker`) and `config.AppID`
  (`steam-achievement-tracker`, used for the keyring service) are different identifiers on purpose.
