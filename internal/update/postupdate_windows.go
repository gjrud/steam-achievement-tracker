//go:build windows

package update

import (
	"errors"
	"log"

	"golang.org/x/sys/windows/registry"
)

// uninstallKeyPath is the per-user Apps & Features entry the NSIS installer writes. The last element is the company name
// plus the product name, the same as UNINST_KEY_NAME in build/windows/nsis/wails_tools.nsh. Change both together.
const uninstallKeyPath = `Software\Microsoft\Windows\CurrentVersion\Uninstall\gjrudSteam Achievement Tracker`

// syncInstalledVersion sets the Apps & Features DisplayVersion of this exe's installer entry to the running version.
// It logs only when it writes, and only the category when it fails. A portable exe has no entry and does nothing.
func syncInstalledVersion(exe, cur string) {
	written, err := syncDisplayVersion(uninstallKeyPath, exe, cur)
	if err != nil {
		log.Printf("update: version sync failed: %s", catOther)
		return
	}
	if written {
		log.Print("update: installed version registered")
	}
}

// syncDisplayVersion writes DisplayVersion under the HKCU key keyPath when its DisplayIcon is exe and the stored version
// is not the running one. It reports whether it wrote. A missing key (no install) or a missing DisplayIcon is not an
// error and writes nothing. The key is opened with the 64-bit view, where the installer writes it.
func syncDisplayVersion(keyPath, exe, cur string) (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, keyPath, registry.QUERY_VALUE|registry.SET_VALUE|registry.WOW64_64KEY)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer k.Close()

	icon, _, err := k.GetStringValue("DisplayIcon")
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	stored, _, err := k.GetStringValue("DisplayVersion")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return false, err
	}
	if !shouldWriteDisplayVersion(icon, exe, stored, cur) {
		return false, nil
	}
	if err := k.SetStringValue("DisplayVersion", displayVersion(cur)); err != nil {
		return false, err
	}
	return true, nil
}
