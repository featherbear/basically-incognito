// Package browser resolves the User Data directory for various
// Chromium-based browsers across Windows, macOS, and Linux.
package browser

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Browser represents a supported Chromium-based browser.
type Browser struct {
	Name        string
	UserDataDir string // absolute path resolved at construction time
}

// knownBrowsers maps OS → browser-name → relative path from $HOME.
var knownBrowsers = map[string]map[string]string{
	"windows": {
		"chrome":   `AppData\Local\Google\Chrome\User Data`,
		"chromium": `AppData\Local\Chromium\User Data`,
		"brave":    `AppData\Local\BraveSoftware\Brave-Browser\User Data`,
		"edge":     `AppData\Local\Microsoft\Edge\User Data`,
		"opera":    `AppData\Roaming\Opera Software\Opera Stable`,
		"vivaldi":  `AppData\Local\Vivaldi\User Data`,
	},
	"darwin": {
		"chrome":   "Library/Application Support/Google/Chrome",
		"chromium": "Library/Application Support/Chromium",
		"brave":    "Library/Application Support/BraveSoftware/Brave-Browser",
		"edge":     "Library/Application Support/Microsoft Edge",
		"opera":    "Library/Application Support/com.operasoftware.Opera",
		"vivaldi":  "Library/Application Support/Vivaldi",
	},
	"linux": {
		"chrome":   ".config/google-chrome",
		"chromium": ".config/chromium",
		"brave":    ".config/BraveSoftware/Brave-Browser",
		"edge":     ".config/microsoft-edge",
		"opera":    ".config/opera",
		"vivaldi":  ".config/vivaldi",
	},
}

// preferenceOrder is the auto-detect order when no browser is specified.
var preferenceOrder = []string{"chrome", "chromium", "brave", "edge", "vivaldi", "opera"}

// SupportedNames returns the list of supported browser names for the current OS.
func SupportedNames() []string {
	osKey := osName()
	paths, ok := knownBrowsers[osKey]
	if !ok {
		return nil
	}
	names := make([]string, 0, len(paths))
	for n := range paths {
		names = append(names, n)
	}
	return names
}

// Detect resolves the User Data directory for the given browser name.
// If name is empty it tries each browser in preference order and returns
// the first one whose directory exists.
func Detect(name string) (*Browser, error) {
	osKey := osName()
	paths, ok := knownBrowsers[osKey]
	if !ok {
		return nil, fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}

	if name != "" {
		name = strings.ToLower(name)
		rel, exists := paths[name]
		if !exists {
			return nil, fmt.Errorf("unknown browser %q for %s; supported: %s",
				name, runtime.GOOS, strings.Join(SupportedNames(), ", "))
		}
		dir := filepath.Join(home, rel)
		if _, err := os.Stat(dir); err != nil {
			return nil, fmt.Errorf("user data directory for %q not found at %s", name, dir)
		}
		return &Browser{Name: name, UserDataDir: dir}, nil
	}

	// Auto-detect
	for _, candidate := range preferenceOrder {
		rel, ok := paths[candidate]
		if !ok {
			continue
		}
		dir := filepath.Join(home, rel)
		if _, err := os.Stat(dir); err == nil {
			return &Browser{Name: candidate, UserDataDir: dir}, nil
		}
	}
	return nil, fmt.Errorf(
		"could not find any Chromium-based browser user data directory; "+
			"use --browser or --user-data-dir to specify one",
	)
}

// FromDir wraps an arbitrary user-data directory path as a Browser.
func FromDir(dir string) (*Browser, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("user data directory does not exist: %s", abs)
	}
	return &Browser{Name: "custom", UserDataDir: abs}, nil
}

func osName() string {
	return strings.ToLower(runtime.GOOS)
}
