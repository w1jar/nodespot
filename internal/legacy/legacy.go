// Package legacy names the previous third-party digital voice stack in one
// place. nodespot refers to it only to install md380-emu from that stack's apt
// repository, to stop and purge old installs (units, packages, apt source),
// and to migrate its own old data (state directories, settings keys, backup
// part ids, database tables). Nothing else in the code base spells these
// names. Settings of the old stack are not imported.
package legacy

import "strings"

// External apt repository that ships md380-emu.
const (
	AptRepoURL   = "http://dvswitch.org/DVSwitch_Repository"
	KeyURL       = "https://dvswitch.org/DVSwitch_Repository/dvswitch-keyring.gpg"
	KeyringPath  = "/usr/share/keyrings/dvswitch-keyring.gpg"
	SourcesPath  = "/etc/apt/sources.list.d/dvswitch.list"
	AptListsGlob = "/var/lib/apt/lists/dvswitch.org*"

	// PackagePrefix is the vendor prefix of the old stack's package names.
	PackagePrefix = "dvswitch"
)

// IsLegacyPackage reports whether the package name belongs to the old stack's
// vendor namespace.
func IsLegacyPackage(name string) bool { return strings.HasPrefix(name, PackagePrefix) }

// Units are the systemd units (without ".service") of the old stack that
// enabling digital modes (and the upgrade purge) stops and disables.
var Units = []string{"analog_bridge", "mmdvm_bridge", "netcheck", "ysfgateway", "p25gateway", "nxdngateway", "stfu"}

// Names of nodespot's own pre-2.0 data that 2.0 migrates, deletes or still accepts.
const (
	OldStateDir       = "/var/lib/nodespot-dvswitch"         // helper state directory
	OldHistoryDir     = "/var/lib/nodespot-dvswitch-history" // helper ini history (deleted on upgrade)
	OldSettingsPrefix = "dvswitch."                          // settings keys
	OldFavoritesTable = "dvswitch_favorites"                 // database table
	OldBackupPart     = "dvswitch"                           // backup part id and archive directory
)

// IniMainRel is the path below /opt of the old stack's main ini file, which
// archives made by 1.x carry (restore ignores it, the archive layout accepts it).
const IniMainRel = "MMDVM_Bridge/DVSwitch.ini"
