package settings

import "testing"

// Discovery is only worth attempting when samo is the system in play. Probing
// the LAN for a samo-server while somebody is configuring Jellyfin would fill
// their Server URL box with the wrong machine.
func TestIsSamoSystem(t *testing.T) {
	cases := map[string]bool{
		"":         true, // fresh install; samo is what it will get
		"samo":     true,
		"SAMO":     true,
		"  samo  ": true,
		"jellyfin": false,
		"emby":     false,
		"plex":     false,
		"subsonic": false,
		"mpd":      false,
	}
	for system, want := range cases {
		if got := isSamoSystem(system); got != want {
			t.Errorf("isSamoSystem(%q) = %v, want %v", system, got, want)
		}
	}
}
