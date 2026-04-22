// Package profile reads Chrome's Local State file to enumerate profiles
// and determine which one was last used.
package profile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Info holds the metadata Chrome stores for a single profile in Local State.
type Info struct {
	DirName     string  // directory name, e.g. "Default", "Profile 1"
	DisplayName string  // human-readable name from info_cache
	ActiveTime  float64 // Unix timestamp of last activity (stored as JSON number)
	IsLastUsed  bool    // true when this is profile.last_used
}

// LocalState is the minimal subset of Chrome's Local State JSON we care about.
type LocalState struct {
	Profile struct {
		LastUsed           string                     `json:"last_used"`
		LastActiveProfiles []string                   `json:"last_active_profiles"`
		InfoCache          map[string]profileMetaJSON `json:"info_cache"`
	} `json:"profile"`
}

// profileMetaJSON mirrors the per-profile object inside info_cache.
// We capture everything so we can faithfully copy it to the new profile entry.
type profileMetaJSON map[string]interface{}

// Load reads and parses the Local State file from userDataDir.
func Load(userDataDir string) (*LocalState, error) {
	path := filepath.Join(userDataDir, "Local State")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read Local State file at %s: %w", path, err)
	}
	var ls LocalState
	if err := json.Unmarshal(data, &ls); err != nil {
		return nil, fmt.Errorf("cannot parse Local State JSON: %w", err)
	}
	return &ls, nil
}

// LastUsedDirName returns the directory name of the last-used profile.
// Strategy (in preference order):
//  1. profile.last_used field
//  2. Profile with the highest active_time in info_cache
//  3. "Default"
func LastUsedDirName(ls *LocalState) string {
	if ls.Profile.LastUsed != "" {
		return ls.Profile.LastUsed
	}
	best := ""
	bestTime := -1.0
	for dir, meta := range ls.Profile.InfoCache {
		t, _ := meta["active_time"].(float64)
		if t > bestTime {
			bestTime = t
			best = dir
		}
	}
	if best != "" {
		return best
	}
	return "Default"
}

// ListProfiles returns all profiles found in both Local State and the filesystem,
// sorted so the last-used profile comes first, then by descending active_time.
func ListProfiles(ls *LocalState, userDataDir string) []Info {
	lastUsed := LastUsedDirName(ls)
	var infos []Info
	for dir, meta := range ls.Profile.InfoCache {
		// Only include directories that actually exist on disk.
		if _, err := os.Stat(filepath.Join(userDataDir, dir)); err != nil {
			continue
		}
		name, _ := meta["name"].(string)
		if name == "" {
			name = dir
		}
		t, _ := meta["active_time"].(float64)
		infos = append(infos, Info{
			DirName:     dir,
			DisplayName: name,
			ActiveTime:  t,
			IsLastUsed:  dir == lastUsed,
		})
	}
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].IsLastUsed != infos[j].IsLastUsed {
			return infos[i].IsLastUsed
		}
		return infos[i].ActiveTime > infos[j].ActiveTime
	})
	return infos
}

// NextProfileDirName returns the next available "Profile N" name that does not
// yet exist on disk. Chrome's convention is Default, Profile 1, Profile 2, …
func NextProfileDirName(userDataDir string) string {
	for n := 1; ; n++ {
		name := fmt.Sprintf("Profile %d", n)
		if _, err := os.Stat(filepath.Join(userDataDir, name)); os.IsNotExist(err) {
			return name
		}
	}
}

// RegisterNewProfile writes the new profile's entry into Local State so that
// Chrome recognises it in the profile switcher immediately.
// The write is performed atomically under an advisory file lock so it is safe
// to call while Chrome is running.
func RegisterNewProfile(userDataDir, newDirName, displayName string, sourceMeta profileMetaJSON) error {
	return modifyLocalState(userDataDir, func(state map[string]interface{}) error {
		profileMap := getOrCreateMap(state, "profile")
		infoCache := getOrCreateMap(profileMap, "info_cache")

		// Build new metadata from source, stripping identity fields.
		newMeta := make(map[string]interface{})
		for k, v := range sourceMeta {
			newMeta[k] = v
		}
		for _, f := range []string{
			"gaia_id", "gaia_name", "gaia_picture_url", "user_name",
			"hosted_domain", "is_consented_primary_account", "is_ephemeral",
			"signin_required", "last_downloaded_gaia_picture_url_with_size",
		} {
			delete(newMeta, f)
		}
		newMeta["name"] = displayName
		newMeta["active_time"] = 0

		infoCache[newDirName] = newMeta
		return nil
	})
}

// SourceMeta retrieves the raw info_cache metadata for a given profile dir name.
// Returns nil if not found.
func SourceMeta(ls *LocalState, dirName string) profileMetaJSON {
	return ls.Profile.InfoCache[dirName]
}

// UnregisterProfile removes a profile entry from Local State's info_cache and
// also removes it from last_active_profiles if present.
// The write is performed atomically under an advisory file lock so it is safe
// to call while Chrome is running.
// It does NOT delete the directory on disk — the caller is responsible for that.
func UnregisterProfile(userDataDir, dirName string) error {
	return modifyLocalState(userDataDir, func(state map[string]interface{}) error {
		profileMap, ok := state["profile"].(map[string]interface{})
		if !ok {
			return nil
		}

		// Remove from info_cache.
		if ic, ok := profileMap["info_cache"].(map[string]interface{}); ok {
			delete(ic, dirName)
		}

		// Remove from last_active_profiles.
		if lap, ok := profileMap["last_active_profiles"].([]interface{}); ok {
			filtered := lap[:0]
			for _, v := range lap {
				if s, ok := v.(string); ok && s == dirName {
					continue
				}
				filtered = append(filtered, v)
			}
			profileMap["last_active_profiles"] = filtered
		}

		// Clear last_used if it points to this profile.
		if lu, ok := profileMap["last_used"].(string); ok && lu == dirName {
			profileMap["last_used"] = ""
		}

		return nil
	})
}

// --- helpers ----------------------------------------------------------------

func getOrCreateMap(parent map[string]interface{}, key string) map[string]interface{} {
	if v, ok := parent[key]; ok {
		if m, ok := v.(map[string]interface{}); ok {
			return m
		}
	}
	m := make(map[string]interface{})
	parent[key] = m
	return m
}
