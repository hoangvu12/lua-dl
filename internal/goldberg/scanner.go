package goldberg

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// interfacePatterns mirrors the regex list from gbe_fork's generate_interfaces.cpp.
var interfacePatterns = []*regexp.Regexp{
	regexp.MustCompile(`STEAMAPPS_INTERFACE_VERSION\d+`),
	regexp.MustCompile(`SteamApps\d+`),
	regexp.MustCompile(`STEAMAPPLIST_INTERFACE_VERSION\d+`),
	regexp.MustCompile(`STEAMAPPTICKET_INTERFACE_VERSION\d+`),
	regexp.MustCompile(`SteamClient\d+`),
	regexp.MustCompile(`STEAMCONTROLLER_INTERFACE_VERSION\d*`),
	regexp.MustCompile(`SteamController\d+`),
	regexp.MustCompile(`SteamFriends\d+`),
	regexp.MustCompile(`SteamGameServerStats\d+`),
	regexp.MustCompile(`SteamGameCoordinator\d+`),
	regexp.MustCompile(`SteamGameServer\d+`),
	regexp.MustCompile(`STEAMHTMLSURFACE_INTERFACE_VERSION_\d+`),
	regexp.MustCompile(`STEAMHTTP_INTERFACE_VERSION\d+`),
	regexp.MustCompile(`SteamInput\d+`),
	regexp.MustCompile(`STEAMINVENTORY_INTERFACE_V\d+`),
	regexp.MustCompile(`SteamMatchMakingServers\d+`),
	regexp.MustCompile(`SteamMatchMaking\d+`),
	regexp.MustCompile(`SteamMatchGameSearch\d+`),
	regexp.MustCompile(`SteamParties\d+`),
	regexp.MustCompile(`STEAMMUSIC_INTERFACE_VERSION\d+`),
	regexp.MustCompile(`STEAMMUSICREMOTE_INTERFACE_VERSION\d+`),
	regexp.MustCompile(`SteamNetworkingMessages\d+`),
	regexp.MustCompile(`SteamNetworkingSockets\d+`),
	regexp.MustCompile(`SteamNetworkingUtils\d+`),
	regexp.MustCompile(`SteamNetworking\d+`),
	regexp.MustCompile(`STEAMPARENTALSETTINGS_INTERFACE_VERSION\d+`),
	regexp.MustCompile(`STEAMREMOTEPLAY_INTERFACE_VERSION\d+`),
	regexp.MustCompile(`STEAMREMOTESTORAGE_INTERFACE_VERSION\d+`),
	regexp.MustCompile(`STEAMSCREENSHOTS_INTERFACE_VERSION\d+`),
	regexp.MustCompile(`STEAMTIMELINE_INTERFACE_V\d+`),
	regexp.MustCompile(`STEAMUGC_INTERFACE_VERSION\d+`),
	regexp.MustCompile(`SteamUser\d+`),
	regexp.MustCompile(`STEAMUSERSTATS_INTERFACE_VERSION\d+`),
	regexp.MustCompile(`SteamUtils\d+`),
	regexp.MustCompile(`STEAMVIDEO_INTERFACE_V\d+`),
	regexp.MustCompile(`STEAMUNIFIEDMESSAGES_INTERFACE_VERSION\d+`),
	regexp.MustCompile(`SteamMasterServerUpdater\d+`),
}

// scanInterfaces walks gameDir for .exe/.dll files and returns a deduplicated,
// sorted list of Steam API interface version strings (e.g. SteamUser021).
func scanInterfaces(gameDir string) ([]string, error) {
	seen := map[string]bool{}
	err := filepath.WalkDir(gameDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext != ".exe" && ext != ".dll" {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for _, re := range interfacePatterns {
			for _, m := range re.FindAll(data, -1) {
				seen[string(m)] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

// findSteamAPIDLLs returns paths of all steam_api.dll / steam_api64.dll files
// found anywhere under gameDir.
func findSteamAPIDLLs(gameDir string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(gameDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		lower := strings.ToLower(d.Name())
		if lower == "steam_api.dll" || lower == "steam_api64.dll" {
			found = append(found, path)
		}
		return nil
	})
	return found, err
}
