package update

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/github"
)

// Asset names as the release publishes them. The deb, rpm and Arch package names and the versioned Windows names are
// the ones on the current release; the AppImage has two names: the old one that the current release still carries (the
// matcher must never pick it) and the renamed one the release job publishes from the next release on.
const (
	assetDeb           = "steam-achievement-tracker.deb"
	assetRPM           = "steam-achievement-tracker.rpm"
	assetArch          = "steam-achievement-tracker.pkg.tar.zst"
	assetOldAppImage   = "steam-achievement-tracker-x86_64.AppImage"
	assetPortable      = "steam-achievement-tracker-1.0.1-windows-amd64-portable.exe"
	assetSetup         = "steam-achievement-tracker-1.0.1-windows-amd64-setup.exe"
	assetSourceTarball = "steam-achievement-tracker-1.0.1-source.tar.gz"
	assetSHA256SUMS    = "SHA256SUMS"
)

// realAssets mirrors a release's asset list after the AppImage rename. stubGitHub serves these names.
var realAssets = []string{
	assetDeb,
	assetRPM,
	assetArch,
	assetOldAppImage,
	linuxAppImage,
	assetPortable,
	assetSetup,
	assetSourceTarball,
	assetSHA256SUMS,
}

func assetsOf(names ...string) []github.ReleaseAsset {
	out := make([]github.ReleaseAsset, len(names))
	for i, n := range names {
		out[i] = github.ReleaseAsset{Name: n}
	}
	return out
}

func indexOf(names []string, want string) int {
	for i, n := range names {
		if n == want {
			return i
		}
	}
	return -1
}

func TestMatchAsset(t *testing.T) {
	cases := []struct {
		name   string
		goos   string
		goarch string
		assets []string
		want   int
	}{
		{"windows amd64 picks portable", "windows", "amd64", realAssets, indexOf(realAssets, assetPortable)},
		{"windows amd64 never picks setup", "windows", "amd64", []string{assetSetup}, -1},
		{"windows amd64 never picks source tarball", "windows", "amd64", []string{assetSourceTarball}, -1},
		{"windows amd64 never picks a linux AppImage", "windows", "amd64", []string{linuxAppImage}, -1},
		{"linux amd64 picks the renamed AppImage", "linux", "amd64", realAssets, indexOf(realAssets, linuxAppImage)},
		{"linux amd64 never picks the old AppImage name", "linux", "amd64", []string{assetOldAppImage}, -1},
		{"linux amd64 never picks deb, rpm or arch", "linux", "amd64", []string{assetDeb, assetRPM, assetArch}, -1},
		{"linux amd64 never picks a portable exe", "linux", "amd64", []string{assetPortable}, -1},
		{"linux amd64 without AppImage", "linux", "amd64", []string{assetSHA256SUMS, assetPortable}, -1},
		{"linux amd64 never picks the source tarball", "linux", "amd64", []string{assetSourceTarball}, -1},
		{"never picks SHA256SUMS", "linux", "amd64", []string{assetSHA256SUMS}, -1},
		{"windows arm64 unsupported", "windows", "arm64", realAssets, -1},
		{"linux arm64 unsupported", "linux", "arm64", realAssets, -1},
		{"darwin unsupported", "darwin", "amd64", realAssets, -1},
		{"empty list", "linux", "amd64", nil, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchAsset(tc.goos, tc.goarch, assetsOf(tc.assets...)); got != tc.want {
				t.Fatalf("matchAsset(%s, %s) = %d, want %d", tc.goos, tc.goarch, got, tc.want)
			}
		})
	}
}

func TestAssetMatcherUsesRequest(t *testing.T) {
	idx := assetMatcher(updater.CheckRequest{Platform: "linux", Arch: "amd64"}, assetsOf(realAssets...))
	if idx != indexOf(realAssets, linuxAppImage) {
		t.Fatalf("assetMatcher = %d", idx)
	}
}

// fakeEnv builds a hostEnv from a map of variables and a fixed executable path. stat is the real os.Stat.
func fakeEnv(goos, exe string, vars map[string]string) hostEnv {
	return hostEnv{
		goos:       goos,
		getenv:     func(k string) string { return vars[k] },
		executable: func() (string, error) { return exe, nil },
		stat:       os.Stat,
		tempDir:    func() string { return "" },
	}
}

func TestAppImageTarget(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "steam-achievement-tracker.AppImage")
	if err := os.WriteFile(img, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirAsImg := filepath.Join(dir, "adir")
	if err := os.Mkdir(dirAsImg, 0o700); err != nil {
		t.Fatal(err)
	}
	mount := filepath.Join(dir, "mount")
	inMount := filepath.Join(mount, "usr", "bin", "steam-achievement-tracker")
	outside := filepath.Join(dir, "usr", "local", "bin", "steam-achievement-tracker")
	missing := filepath.Join(dir, "missing.AppImage")

	cases := []struct {
		name string
		exe  string
		vars map[string]string
		want string
	}{
		{"AppImage run resolves to the AppImage", inMount, map[string]string{"APPIMAGE": img, "APPDIR": mount}, img},
		{"APPDIR with trailing separator", inMount, map[string]string{"APPIMAGE": img, "APPDIR": mount + string(filepath.Separator)}, img},
		{"inherited APPIMAGE in a package install is not an AppImage run", outside, map[string]string{"APPIMAGE": img, "APPDIR": mount}, ""},
		{"no APPDIR", inMount, map[string]string{"APPIMAGE": img}, ""},
		{"no APPIMAGE", inMount, map[string]string{"APPDIR": mount}, ""},
		{"APPIMAGE is a directory", inMount, map[string]string{"APPIMAGE": dirAsImg, "APPDIR": mount}, ""},
		{"APPIMAGE missing", inMount, map[string]string{"APPIMAGE": missing, "APPDIR": mount}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := appImageTarget(fakeEnv("linux", tc.exe, tc.vars)); got != tc.want {
				t.Fatalf("appImageTarget = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("executable lookup failure", func(t *testing.T) {
		env := fakeEnv("linux", inMount, map[string]string{"APPIMAGE": img, "APPDIR": mount})
		env.executable = func() (string, error) { return "", errors.New("no exe") }
		if got := appImageTarget(env); got != "" {
			t.Fatalf("appImageTarget = %q, want empty", got)
		}
	})
}

func TestHostRefusal(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "steam-achievement-tracker.AppImage")
	if err := os.WriteFile(img, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	mount := filepath.Join(dir, "mount")
	inMount := filepath.Join(mount, "usr", "bin", "steam-achievement-tracker")
	pkgExe := filepath.Join(dir, "usr", "local", "bin", "steam-achievement-tracker")
	noVars := map[string]string{}
	appImageVars := map[string]string{"APPIMAGE": img, "APPDIR": mount}

	cases := []struct {
		name string
		env  hostEnv
		want string
	}{
		{"windows installs", fakeEnv("windows", `C:\app\sat.exe`, noVars), ""},
		{"linux AppImage installs", fakeEnv("linux", inMount, appImageVars), ""},
		{"linux package is notify-only", fakeEnv("linux", pkgExe, noVars), reasonPackage},
		{"linux inherited APPIMAGE is notify-only", fakeEnv("linux", pkgExe, appImageVars), reasonPackage},
		{"darwin is unsupported", fakeEnv("darwin", "/Applications/x", noVars), reasonUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hostRefusal(tc.env); got != tc.want {
				t.Fatalf("hostRefusal = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInstallRefusal(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "steam-achievement-tracker.AppImage")
	if err := os.WriteFile(img, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	mount := filepath.Join(dir, "mount")
	inMount := filepath.Join(mount, "usr", "bin", "steam-achievement-tracker")
	pkgExe := filepath.Join(dir, "usr", "local", "bin", "steam-achievement-tracker")
	good := &updater.Verification{DigestAlgo: "sha256", Digest: make([]byte, sha256.Size)}
	noVars := map[string]string{}
	appImageVars := map[string]string{"APPIMAGE": img, "APPDIR": mount}

	cases := []struct {
		name string
		v    *updater.Verification
		env  hostEnv
		want string
	}{
		{"windows with checksum installs", good, fakeEnv("windows", `C:\app\sat.exe`, noVars), ""},
		{"nil verification refused on windows", nil, fakeEnv("windows", `C:\app\sat.exe`, noVars), reasonNoChecksum},
		{"sha512 refused", &updater.Verification{DigestAlgo: "sha512", Digest: make([]byte, 64)}, fakeEnv("windows", `C:\a.exe`, noVars), reasonNoChecksum},
		{"empty algo refused", &updater.Verification{Digest: make([]byte, sha256.Size)}, fakeEnv("windows", `C:\a.exe`, noVars), reasonNoChecksum},
		{"31-byte digest refused", &updater.Verification{DigestAlgo: "sha256", Digest: make([]byte, 31)}, fakeEnv("windows", `C:\a.exe`, noVars), reasonNoChecksum},
		{"33-byte digest refused", &updater.Verification{DigestAlgo: "sha256", Digest: make([]byte, 33)}, fakeEnv("windows", `C:\a.exe`, noVars), reasonNoChecksum},
		{"nil verification refused on linux AppImage", nil, fakeEnv("linux", inMount, appImageVars), reasonNoChecksum},
		{"linux AppImage installs", good, fakeEnv("linux", inMount, appImageVars), ""},
		{"linux package install is notify-only", good, fakeEnv("linux", pkgExe, noVars), reasonPackage},
		{"linux package install without checksum shows the package reason", nil, fakeEnv("linux", pkgExe, noVars), reasonPackage},
		{"linux inherited APPIMAGE is notify-only", good, fakeEnv("linux", pkgExe, appImageVars), reasonPackage},
		{"other platform is notify-only", good, fakeEnv("darwin", "/Applications/x", noVars), reasonUnsupported},
		{"other platform without checksum shows the platform reason", nil, fakeEnv("darwin", "/Applications/x", noVars), reasonUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := installRefusal(tc.v, tc.env); got != tc.want {
				t.Fatalf("installRefusal = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReleasePageURL(t *testing.T) {
	cases := map[string]string{
		"0.3.0":      "https://github.com/gjrud/steam-achievement-tracker/releases/tag/v0.3.0",
		"0.3.0-rc.1": "https://github.com/gjrud/steam-achievement-tracker/releases/tag/v0.3.0-rc.1",
		"":           "",
		"v0.3.0":     "",
		"0.3":        "",
		"0.3.0/../x": "",
		"0.3.0?x=1":  "",
		"0.3.0\n":    "",
	}
	for in, want := range cases {
		if got := releasePageURL(in); got != want {
			t.Errorf("releasePageURL(%q) = %q, want %q", in, got, want)
		}
	}
}
