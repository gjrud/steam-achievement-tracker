package update

import (
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/gjrud/steam-achievement-tracker/internal/version"
	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/github"
)

const (
	repository    = "gjrud/steam-achievement-tracker"
	checksumAsset = "SHA256SUMS"

	// linuxAppImage is the release asset the Linux updater selects. The release job renames the AppImage to this name
	// before it writes SHA256SUMS.
	linuxAppImage = "steam-achievement-tracker-linux-x86_64.AppImage"
	// windowsPortableSuffix matches the portable exe, whose name carries the version (-<version>-windows-amd64-portable.exe).
	windowsPortableSuffix = "-windows-amd64-portable.exe"
)

// matchAsset returns the index of the asset the updater installs or checks against on goos/goarch, or -1. Linux always
// resolves to the AppImage, so a package install can still learn that a newer release exists (notify-only).
func matchAsset(goos, goarch string, assets []github.ReleaseAsset) int {
	for i, a := range assets {
		switch {
		case goos == "windows" && goarch == "amd64" && strings.HasSuffix(a.Name, windowsPortableSuffix):
			return i
		case goos == "linux" && goarch == "amd64" && a.Name == linuxAppImage:
			return i
		}
	}
	return -1
}

// assetMatcher adapts matchAsset to github.AssetMatcher.
func assetMatcher(req updater.CheckRequest, assets []github.ReleaseAsset) int {
	return matchAsset(req.Platform, req.Arch, assets)
}

// hostEnv is what the install gate reads from the process. Tests replace it; production uses realEnv.
type hostEnv struct {
	goos       string
	getenv     func(string) string
	executable func() (string, error)
	stat       func(string) (fs.FileInfo, error)
}

func realEnv() hostEnv {
	return hostEnv{goos: runtime.GOOS, getenv: os.Getenv, executable: os.Executable, stat: os.Stat}
}

// appImageTarget reproduces the framework's Linux target rule (pkg/updater spawn.go, appImageOr). It returns $APPIMAGE
// only when the running executable lives under $APPDIR and $APPIMAGE is a regular file. An inherited APPIMAGE in a
// process that is not an AppImage run fails the APPDIR check, so it returns "" and installs stay off.
func appImageTarget(env hostEnv) string {
	img, mount := env.getenv("APPIMAGE"), env.getenv("APPDIR")
	if img == "" || mount == "" {
		return ""
	}
	exe, err := env.executable()
	if err != nil || !strings.HasPrefix(exe, filepath.Clean(mount)+string(filepath.Separator)) {
		return ""
	}
	info, err := env.stat(img)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return img
}

// Notify-only reasons. These are shown to the user as plain sentences.
const (
	reasonNoChecksum  = "This release has no verified checksum, so it can only be downloaded from the release page."
	reasonPackage     = "This copy was installed from a package. Update it with your package manager or download the new release from the release page."
	reasonUnsupported = "In-app updates are not available on this platform. Download the new release from the release page."
)

// hostRefusal returns the reason this process cannot install in place, or "" when it can: Windows always can, Linux
// only from an AppImage run, and any other platform never. It does not depend on the release.
func hostRefusal(env hostEnv) string {
	switch env.goos {
	case "windows":
		return ""
	case "linux":
		if appImageTarget(env) != "" {
			return ""
		}
		return reasonPackage
	}
	return reasonUnsupported
}

// installRefusal returns the reason an in-app install is refused for a release whose verification block is v, or ""
// when it may be installed. The host condition is checked first, so a package install shows the package reason even
// when the release has no checksum. Refusal is notify-only: the banner shows the release page instead of Install.
func installRefusal(v *updater.Verification, env hostEnv) string {
	if reason := hostRefusal(env); reason != "" {
		return reason
	}
	if v == nil || v.DigestAlgo != "sha256" || len(v.Digest) != sha256.Size {
		return reasonNoChecksum
	}
	return ""
}

// releasePageURL builds the release page link from a validated version. An invalid version gives "".
func releasePageURL(v string) string {
	valid, ok := version.Valid(v)
	if !ok {
		return ""
	}
	return "https://github.com/" + repository + "/releases/tag/v" + valid
}
