// Package cloner copies the desired files from a source Chrome profile directory
// into a freshly created destination profile directory.
//
// Only three categories are copied:
//   - Bookmarks  (Bookmarks JSON file)
//   - History    (History, Favicons, Top Sites — SQLite)
//   - Extensions (Extensions/ directory, enabled extensions only)
//
// SQLite database files are copied using "VACUUM INTO" via the sqlitecopy
// package, which produces a consistent hot-copy even while Chrome holds a
// write lock on the source file.
package cloner

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/user/chrome-profile-cloner/internal/progress"
	"github.com/user/chrome-profile-cloner/internal/sqlitecopy"
)

// excludedNames is the set of file/directory names that must NEVER be copied,
// regardless of how they are encountered during a directory walk.
// _metadata/ is excluded because verified_contents.json inside it is
// cryptographically tied to the source profile path. Copying it to a new
// profile causes Chrome to fail the integrity check and show "extension failed
// to load properly". Chrome regenerates _metadata/ on first use.
var excludedNames = map[string]struct{}{
	// Cookies
	"Cookies":         {},
	"Cookies-journal": {},

	// Passwords
	"Login Data":                     {},
	"Login Data-journal":             {},
	"Login Data For Account":         {},
	"Login Data For Account-journal": {},

	// Cache
	"Cache":      {},
	"Code Cache": {},
	"GPUCache":   {},
	"ShaderCache": {},

	// Session / tab restore
	"Current Session": {},
	"Current Tabs":    {},
	"Last Session":    {},
	"Last Tabs":       {},

	// Sync
	"Sync Data":               {},
	"Sync Extension Settings": {},
	"SyncData.sqlite3":        {},


	// Lock files
	"SingletonLock":   {},
	"SingletonSocket": {},
	"SingletonCookie": {},

	// Extension settings / state — session and identity-specific
	"Local Extension Settings": {},
	"Extension Rules":          {},
	"Extension Scripts":        {},
	"Extension State":          {},

	// Favicons / Top Sites — not copied
	"Favicons":          {},
	"Favicons-journal":  {},
	"Top Sites":         {},
	"Top Sites-journal": {},

	// Autofill / search engines
	"Web Data":         {},
	"Web Data-journal": {},

	// Network directory — cookies, trust tokens, transport security
	"Network": {},

	// Misc transient / site-specific storage
	"Network Action Predictor":         {},
	"Network Action Predictor-journal": {},
	"QuotaManager":                     {},
	"QuotaManager-journal":             {},
	"databases":                        {},
	"IndexedDB":                        {},
	"Service Worker":                   {},
	"blob_storage":                     {},
	"shared_proto_db":                  {},
	"GCM Store":                        {},
	"data_reduction_proxy_leveldb":     {},
}

// Options controls the behaviour of Clone.
type Options struct {
	DryRun       bool // when true nothing is written to disk
	NoBookmarks  bool // when true the Bookmarks file is not copied
	NoHistory    bool // when true the History SQLite database is not copied
	NoExtensions bool // when true the Extensions directory is not copied
}

// Result summarises what was (or would be) done.
type Result struct {
	ItemsCopied          int
	ItemsSkipped         int
	ExtensionsCopied     int
	ExtensionsSkipped    int
}

// WritePreferences writes both Preferences and Secure Preferences into
// dstProfile by copying the source files wholesale and then patching:
//   - profile.name        → displayName
//   - extensions.settings → only enabled extensions whose code was copied
//
// Writing the full source Preferences (rather than a minimal stub) prevents
// Chrome from considering the file malformed and overwriting it with defaults
// on first launch. Both files are written because Chrome cross-checks them.
//
// opts is used to determine which categories were cloned so that the
// Preferences file is patched consistently — e.g. when NoExtensions is set,
// extensions.settings is written as an empty map without scanning the disk.
func WritePreferences(srcProfile, dstProfile, displayName string, opts Options) error {
	// Collect the IDs of extensions that were actually copied to dstProfile.
	// When NoExtensions is set we skip the disk scan — copiedIDs stays empty,
	// which causes extensions.settings to be written as {} below.
	copiedIDs := map[string]struct{}{}
	if !opts.NoExtensions {
		dstExtDir := filepath.Join(dstProfile, "Extensions")
		if entries, err := os.ReadDir(dstExtDir); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					copiedIDs[e.Name()] = struct{}{}
				}
			}
		}
	}

	for _, fname := range []string{"Secure Preferences", "Preferences"} {
		srcPath := filepath.Join(srcProfile, fname)
		dstPath := filepath.Join(dstProfile, fname)

		data, err := os.ReadFile(srcPath)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", fname, err)
		}

		// Parse as a raw map so we preserve every field Chrome expects.
		var prefs map[string]interface{}
		if err := json.Unmarshal(data, &prefs); err != nil {
			return fmt.Errorf("parse %s: %w", fname, err)
		}

		// Strip session restore keys so Chrome doesn't offer to restore tabs.
		delete(prefs, "sessions")
		delete(prefs, "session")

		// Strip top-level identity, sync and account keys entirely.
		topLevelStrip := []string{
			"account_info",
			"account_tracker_service_last_update",
			"account_values",
			"enterprise_profile_guid",
			"gaia_cookie",
			"google",
			"signin",
			"sync",
			"should_read_incoming_syncing_theme_prefs",
			"syncing_theme_prefs_migrated_to_non_syncing",
			"total_passwords_available_for_account",
			"total_passwords_available_for_profile",
		}
		for _, k := range topLevelStrip {
			delete(prefs, k)
		}

		// Patch profile sub-keys: set name, strip avatar and identity fields.
		profileMap := getOrCreateMapIn(prefs, "profile")
		profileMap["name"] = displayName
		// Force a clean exit type so Chrome doesn't show the "didn't shut down
		// correctly" restore banner on first launch of the cloned profile.
		profileMap["exit_type"] = "Normal"
		profileMap["exited_cleanly"] = true

		// Set a dark monochromatic theme so the cloned profile is visually distinct.
		// We only write color_scheme2 (dark mode) — the safest minimal change.
		// Avoid writing color_variant2/user_color2 as unsupported values can crash Chrome.
		browserMap := getOrCreateMapIn(prefs, "browser")
		existingTheme, _ := browserMap["theme"].(map[string]interface{})
		if existingTheme == nil {
			existingTheme = map[string]interface{}{}
		}
		existingTheme["color_scheme2"] = 2         // dark mode
		existingTheme["follows_system_colors"] = false
		delete(existingTheme, "saved_local_theme")   // clear proto-encoded theme
		browserMap["theme"] = existingTheme

		// Clear any extension-based theme so it doesn't override ours.
		extMap2 := getOrCreateMapIn(prefs, "extensions")
		delete(extMap2, "theme")
		profileSubStrip := []string{
			"avatar_index",
			"family_member_role",
			"gaia_name",
			"gaia_picture_url",
			"gaia_id",
			"managed_user_id",
			"password_hash_data_list",
			"user_name",
			"were_old_google_logins_removed",
		}
		for _, k := range profileSubStrip {
			delete(profileMap, k)
		}

		// Build extension settings from Secure Preferences (authoritative source)
		// cross-referenced against extensions actually copied to dstProfile.
		// Preferences.extensions.settings is always empty in modern Chrome —
		// Chrome only writes extension state to Secure Preferences (HMAC-protected).
		// We read from Secure Preferences but write the filtered result into
		// Preferences only (since we can't produce valid HMACs for Secure Preferences).
		extMap := getOrCreateMapIn(prefs, "extensions")
		filtered := make(map[string]interface{}, len(copiedIDs))
		if secRaw, err := os.ReadFile(filepath.Join(srcProfile, "Secure Preferences")); err == nil {
			var secPrefs map[string]interface{}
			if json.Unmarshal(secRaw, &secPrefs) == nil {
				if secExt, ok := secPrefs["extensions"].(map[string]interface{}); ok {
					if secSettings, ok := secExt["settings"].(map[string]interface{}); ok {
						for id, v := range secSettings {
							// Only include extensions whose directory was copied.
							if _, copied := copiedIDs[id]; !copied {
								continue
							}
							entry, ok := v.(map[string]interface{})
							if !ok {
								continue
							}
							// Skip disabled extensions.
							if state, ok := entry["state"].(float64); ok && state == 0 {
								continue
							}
							if dr, ok := entry["disable_reasons"].([]interface{}); ok && len(dr) > 0 {
								continue
							}
							filtered[id] = entry
						}
					}
				}
			}
		}
		extMap["settings"] = filtered

		out, err := json.MarshalIndent(prefs, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal %s: %w", fname, err)
		}
		if err := os.MkdirAll(dstProfile, 0755); err != nil {
			return fmt.Errorf("create profile dir: %w", err)
		}
		if err := os.WriteFile(dstPath, out, 0644); err != nil {
			return fmt.Errorf("write %s: %w", fname, err)
		}
	}
	return nil
}

func getOrCreateMapIn(parent map[string]interface{}, key string) map[string]interface{} {
	if v, ok := parent[key]; ok {
		if m, ok := v.(map[string]interface{}); ok {
			return m
		}
	}
	m := make(map[string]interface{})
	parent[key] = m
	return m
}

// ExtensionInfo holds display information about a single extension.
type ExtensionInfo struct {
	ID       string
	Name     string
	Version  string
	Location string
	Enabled  bool
}

// locationLabel maps Chrome's extension location integer to a human-readable string.
// https://source.chromium.org/chromium/chromium/src/+/main:extensions/common/mojom/manifest.mojom
func locationLabel(loc int) string {
	switch loc {
	case 1:
		return "webstore"
	case 2:
		return "webstore-update"
	case 3:
		return "unpacked"
	case 4:
		return "commandline"
	case 5:
		return "component"
	case 6:
		return "external"
	case 7:
		return "policy"
	case 8:
		return "policy-update"
	case 9:
		return "sideloaded"
	case 10:
		return "internal"
	default:
		return "unknown"
	}
}

// ListExtensions returns all extensions (enabled and disabled) for the given
// profile directory that have code present on disk, sorted by name.
// It reads from Secure Preferences (falling back to Preferences).
func ListExtensions(profileDir string) []ExtensionInfo {
	// Try Secure Preferences first, then Preferences.
	settings, _ := readExtensionSettings(filepath.Join(profileDir, "Secure Preferences"))
	if len(settings) == 0 {
		settings, _ = readExtensionSettings(filepath.Join(profileDir, "Preferences"))
	}

	extDir := filepath.Join(profileDir, "Extensions")
	onDisk := map[string]struct{}{}
	if entries, err := os.ReadDir(extDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				onDisk[e.Name()] = struct{}{}
			}
		}
	}

	var result []ExtensionInfo
	for id, ext := range settings {
		// Skip extensions with no code on disk (components, built-ins).
		if _, ok := onDisk[id]; !ok {
			continue
		}
		// Resolve name and version from manifest on disk.
		name, version := resolveExtensionNameAndVersion(profileDir, id)
		result = append(result, ExtensionInfo{
			ID:       id,
			Name:     name,
			Version:  version,
			Location: locationLabel(ext.Location),
			Enabled:  ext.isEnabled(),
		})
	}

	// Sort by name for consistent output.
	for i := 1; i < len(result); i++ {
		for j := i; j > 0 && result[j].Name < result[j-1].Name; j-- {
			result[j], result[j-1] = result[j-1], result[j]
		}
	}
	return result
}

// resolveExtensionNameAndVersion reads the manifest.json from the extension's
// versioned directory and returns (name, version). Localised __MSG_key__ names
// are resolved from _locales. Falls back to (id, "") if nothing resolves.
func resolveExtensionNameAndVersion(profileDir, id string) (name, version string) {
	extDir := filepath.Join(profileDir, "Extensions", id)
	entries, err := os.ReadDir(extDir)
	if err != nil {
		return id, ""
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		versionDir := filepath.Join(extDir, e.Name())

		manifestData, err := os.ReadFile(filepath.Join(versionDir, "manifest.json"))
		if err != nil {
			continue
		}
		var m struct {
			Name          string `json:"name"`
			Version       string `json:"version"`
			DefaultLocale string `json:"default_locale"`
		}
		if err := json.Unmarshal(manifestData, &m); err != nil {
			continue
		}
		if m.Name == "" {
			continue
		}

		ver := m.Version

		// Resolve __MSG_key__ localisation placeholders.
		if strings.HasPrefix(m.Name, "__MSG_") && strings.HasSuffix(m.Name, "__") {
			msgKey := m.Name[6 : len(m.Name)-2]
			locales := []string{m.DefaultLocale, "en"}
			localeDir := filepath.Join(versionDir, "_locales")
			if others, err := os.ReadDir(localeDir); err == nil {
				for _, o := range others {
					locales = append(locales, o.Name())
				}
			}
			resolved := ""
			for _, locale := range locales {
				if locale == "" {
					continue
				}
				msgData, err := os.ReadFile(filepath.Join(localeDir, locale, "messages.json"))
				if err != nil {
					continue
				}
				var messages map[string]struct {
					Message string `json:"message"`
				}
				if err := json.Unmarshal(msgData, &messages); err != nil {
					continue
				}
				for k, v := range messages {
					if strings.EqualFold(k, msgKey) && v.Message != "" {
						resolved = v.Message
						break
					}
				}
				if resolved != "" {
					break
				}
			}
			if resolved != "" {
				return resolved, ver
			}
			return id, ver
		}
		return m.Name, ver
	}
	return id, ""
}

// Clone copies bookmarks, history, and enabled extensions from srcProfile
// into dstProfile.
func Clone(srcProfile, dstProfile string, opts Options) (Result, error) {
	var res Result

	if !opts.DryRun {
		if err := os.MkdirAll(dstProfile, 0755); err != nil {
			return res, fmt.Errorf("create destination profile dir: %w", err)
		}
	}

	// ── 1. Bookmarks ─────────────────────────────────────────────────────────
	if opts.NoBookmarks {
		if opts.DryRun {
			fmt.Println("  [dry-run] skipping Bookmarks (--no-bookmarks)")
		} else {
			fmt.Println("  Skipped: Bookmarks (--no-bookmarks)")
		}
		res.ItemsSkipped++
	} else {
		if err := copyItem(srcProfile, dstProfile, "Bookmarks", false, opts, &res); err != nil {
			return res, err
		}
	}

	// ── 2. History (SQLite — hot-copied via VACUUM INTO) ─────────────────────
	if opts.NoHistory {
		if opts.DryRun {
			fmt.Println("  [dry-run] skipping History (--no-history)")
		} else {
			fmt.Println("  Skipped: History (--no-history)")
		}
		res.ItemsSkipped++
	} else {
		if err := copyItem(srcProfile, dstProfile, "History", false, opts, &res); err != nil {
			return res, err
		}
	}

	// ── 3. Extensions (enabled only) ─────────────────────────────────────────
	if opts.NoExtensions {
		if opts.DryRun {
			fmt.Println("  [dry-run] skipping Extensions (--no-extensions)")
		} else {
			fmt.Println("  Skipped: Extensions (--no-extensions)")
		}
	} else {
		extRes, err := copyEnabledExtensions(srcProfile, dstProfile, opts)
		if err != nil {
			return res, err
		}
		res.ExtensionsCopied = extRes.ExtensionsCopied
		res.ExtensionsSkipped = extRes.ExtensionsSkipped
	}

	return res, nil
}

// copyItem copies a single named file or directory from src to dst profile.
func copyItem(srcProfile, dstProfile, name string, isDir bool, opts Options, res *Result) error {
	src := filepath.Join(srcProfile, name)
	dst := filepath.Join(dstProfile, name)

	info, err := os.Stat(src)
	if os.IsNotExist(err) {
		return nil // not every profile has every file
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}

	if opts.DryRun {
		fmt.Printf("  [dry-run] would copy %s\n", name)
		res.ItemsCopied++
		return nil
	}

	var copyErr error
	if info.IsDir() || isDir {
		copyErr = copyDir(src, dst)
	} else {
		copyErr = copyFile(src, dst)
	}
	if copyErr != nil {
		return fmt.Errorf("copy %s: %w", name, copyErr)
	}
	fmt.Printf("  Copied: %s\n", name)
	res.ItemsCopied++
	return nil
}

// ---------------------------------------------------------------------------
// Extension filtering
// ---------------------------------------------------------------------------

// extensionEntry mirrors the per-extension object inside extensions.settings.
// State is a pointer so we can distinguish "absent" (nil = enabled by default
// in Chrome) from explicitly set to 0 (disabled). Chrome only writes the state
// field when an extension is disabled; enabled extensions have no state field.
// DisableReasons is a list of reason codes; a non-empty list means the
// extension is disabled regardless of the state field value.
type extensionEntry struct {
	State          *int  `json:"state"`
	Location       int   `json:"location"`
	DisableReasons []int `json:"disable_reasons"`
}

// isEnabled returns true if the extension is enabled — i.e. state is not
// explicitly 0 AND disable_reasons is empty.
func (e extensionEntry) isEnabled() bool {
	if e.State != nil && *e.State == 0 {
		return false
	}
	return len(e.DisableReasons) == 0
}

// extensionSettings is the minimal shape we care about in Preferences /
// Secure Preferences.
type extensionSettings struct {
	Extensions struct {
		Settings map[string]extensionEntry `json:"settings"`
	} `json:"extensions"`
}

// enabledExtensionIDs reads extension state from the source profile's
// Preferences file (with a fallback to Secure Preferences) and returns the
// set of extension IDs that are enabled.
//
// Chrome stores the authoritative enabled/disabled state in
// extensions.settings[id].state (1 = enabled, 0 = disabled) in Preferences.
// In some Chrome versions the same data is mirrored in Secure Preferences.
// We try Preferences first; if its settings map is empty (Chrome may have
// only written to Secure Preferences) we try Secure Preferences.
//
// If neither file exists or both are unreadable, we return nil — the caller
// will copy all extensions as a safe fallback.
func enabledExtensionIDs(srcProfile string) (map[string]bool, error) {
	settings, err := readExtensionSettings(filepath.Join(srcProfile, "Preferences"))
	if err != nil {
		return nil, err
	}

	// Fall back to Secure Preferences if settings map is empty.
	if len(settings) == 0 {
		secureSettings, err := readExtensionSettings(filepath.Join(srcProfile, "Secure Preferences"))
		if err != nil {
			return nil, err
		}
		settings = secureSettings
	}

	// If still empty, copy all extensions.
	if len(settings) == 0 {
		return nil, nil
	}

	enabled := make(map[string]bool, len(settings))
	for id, ext := range settings {
		if ext.isEnabled() {
			enabled[id] = true
		}
	}
	return enabled, nil
}

// readExtensionSettings parses extensions.settings from a Preferences-format
// JSON file. Returns an empty map (not an error) if the file does not exist
// or does not contain the expected keys.
func readExtensionSettings(path string) (map[string]extensionEntry, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var prefs extensionSettings
	if err := json.Unmarshal(data, &prefs); err != nil {
		// Malformed — treat as empty, caller will fall back.
		return nil, nil
	}
	return prefs.Extensions.Settings, nil
}

// copyEnabledExtensions copies only the enabled extensions from the source
// Extensions/ directory into the destination concurrently.
// Disabled extensions are skipped.
func copyEnabledExtensions(srcProfile, dstProfile string, opts Options) (Result, error) {
	var res Result

	srcExtDir := filepath.Join(srcProfile, "Extensions")
	if _, err := os.Stat(srcExtDir); os.IsNotExist(err) {
		return res, nil
	}

	enabled, err := enabledExtensionIDs(srcProfile)
	if err != nil {
		return res, err
	}

	entries, err := os.ReadDir(srcExtDir)
	if err != nil {
		return res, fmt.Errorf("read Extensions dir: %w", err)
	}

	dstExtDir := filepath.Join(dstProfile, "Extensions")

	// Partition entries into to-copy and to-skip.
	type copyJob struct{ id, src, dst string }
	var jobs []copyJob

	for _, entry := range entries {
		id := entry.Name()
		if enabled != nil && !enabled[id] {
			if opts.DryRun {
				fmt.Printf("  [dry-run] would skip  Extensions/%s (disabled)\n", id)
			} else {
				fmt.Printf("  Skipped extension: %s (disabled)\n", id)
			}
			res.ExtensionsSkipped++
			continue
		}
		if opts.DryRun {
			fmt.Printf("  [dry-run] would copy  Extensions/%s\n", id)
			res.ExtensionsCopied++
			continue
		}
		jobs = append(jobs, copyJob{
			id:  id,
			src: filepath.Join(srcExtDir, id),
			dst: filepath.Join(dstExtDir, id),
		})
	}

	if opts.DryRun || len(jobs) == 0 {
		return res, nil
	}

	// Copy extensions concurrently with a bounded worker pool.
	// Use min(numCPU, numJobs, 8) workers to avoid overwhelming the OS with
	// parallel directory walks.
	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	if workers > len(jobs) {
		workers = len(jobs)
	}

	type result struct {
		id  string
		err error
	}

	jobCh := make(chan copyJob, len(jobs))
	for _, j := range jobs {
		jobCh <- j
	}
	close(jobCh)

	bar := progress.New(len(jobs), "Extensions")
	bar.Inc() // draw initial state

	resultCh := make(chan result, len(jobs))
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobCh {
				err := cloneDir(j.src, j.dst)
				resultCh <- result{id: j.id, err: err}
			}
		}()
	}

	// Close resultCh once all workers are done.
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	var firstErr error
	for r := range resultCh {
		if r.err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("copy extension %s: %w", r.id, r.err)
			}
		} else {
			res.ExtensionsCopied++
		}
		bar.Inc()
	}
	bar.Done()

	return res, firstErr
}

// ---------------------------------------------------------------------------
// Low-level copy helpers
// ---------------------------------------------------------------------------

// copyFile copies a single file from src to dst, preserving permissions.
// If the file is a SQLite database it is copied via VACUUM INTO (a consistent
// hot-copy safe while Chrome is running); otherwise copyFileRaw is used.
func copyFile(src, dst string) error {
	if sqlitecopy.IsSQLite(src) {
		return sqlitecopy.Copy(src, dst)
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	return copyFileRaw(src, dst, info.Mode())
}

// copyDir recursively copies the directory tree rooted at src to dst.
// It skips any entry whose name appears in excludedNames.
// Files inside directories are copied with copyFileRaw (no SQLite check) —
// extension directories contain only JS/CSS/JSON/HTML/PNG files, never SQLite.
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		base := filepath.Base(path)
		if _, excluded := excludedNames[base]; excluded && path != src {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFileRaw(path, target, info.Mode())
	})
}

// copyFileRaw copies a file byte-for-byte without any SQLite detection.
// Use this for files that are known to never be SQLite databases (e.g.
// extension source files: JS, CSS, JSON, HTML, PNG, etc.).
func copyFileRaw(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
