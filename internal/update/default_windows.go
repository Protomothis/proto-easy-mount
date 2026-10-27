package update

import "golang.org/x/sys/windows/registry"

// installerKey holds what the installer chose (installer/proto-easy-mount.iss):
// HKCU for a per-user install, HKLM for all users.
const installerKey = `Software\Protomothis\ProtoEasyMount`

// InstallerDefault is whether the installer left automatic update checks
// on; true when it said nothing.
func InstallerDefault() bool {
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		k, err := registry.OpenKey(root, installerKey, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		v, _, err := k.GetIntegerValue("UpdateCheck")
		k.Close()
		if err == nil {
			return v != 0
		}
	}
	return true
}
