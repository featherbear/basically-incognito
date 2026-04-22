// Package launcher finds the browser executable for the current OS and
// launches it with a specific profile directory, then waits for it to exit.
package launcher

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// knownExecutables maps OS → browser-name → candidate executable paths/names.
// Each entry is tried in order; the first one found on disk (or in PATH) wins.
var knownExecutables = map[string]map[string][]string{
	"windows": {
		"chrome":   {`C:\Program Files\Google\Chrome\Application\chrome.exe`, `C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`},
		"chromium": {`C:\Program Files\Chromium\Application\chrome.exe`},
		"brave":    {`C:\Program Files\BraveSoftware\Brave-Browser\Application\brave.exe`},
		"edge":     {`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`, `C:\Program Files\Microsoft\Edge\Application\msedge.exe`},
		"opera":    {`C:\Program Files\Opera\launcher.exe`},
		"vivaldi":  {`C:\Program Files\Vivaldi\Application\vivaldi.exe`},
	},
	"darwin": {
		"chrome":   {"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"},
		"chromium": {"/Applications/Chromium.app/Contents/MacOS/Chromium"},
		"brave":    {"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser"},
		"edge":     {"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge"},
		"opera":    {"/Applications/Opera.app/Contents/MacOS/Opera"},
		"vivaldi":  {"/Applications/Vivaldi.app/Contents/MacOS/Vivaldi"},
	},
	"linux": {
		"chrome":   {"google-chrome", "google-chrome-stable"},
		"chromium": {"chromium-browser", "chromium"},
		"brave":    {"brave-browser", "brave"},
		"edge":     {"microsoft-edge", "microsoft-edge-stable"},
		"opera":    {"opera"},
		"vivaldi":  {"vivaldi-stable", "vivaldi"},
	},
}

// FindExecutable resolves the absolute path to the browser binary.
// browserName must match the keys in knownExecutables (e.g. "chrome").
// Returns an error if no candidate is found.
func FindExecutable(browserName string) (string, error) {
	osKey := strings.ToLower(runtime.GOOS)
	byBrowser, ok := knownExecutables[osKey]
	if !ok {
		return "", fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}

	candidates, ok := byBrowser[strings.ToLower(browserName)]
	if !ok {
		return "", fmt.Errorf("no known executable for browser %q on %s", browserName, runtime.GOOS)
	}

	for _, c := range candidates {
		if filepath.IsAbs(c) {
			// Absolute path — check existence directly.
			if _, err := os.Stat(c); err == nil {
				return c, nil
			}
		} else {
			// Bare name — search PATH.
			if path, err := exec.LookPath(c); err == nil {
				return path, nil
			}
		}
	}

	return "", fmt.Errorf(
		"could not find %s executable; tried: %s",
		browserName, strings.Join(candidates, ", "),
	)
}

// Session represents a running browser instance.
type Session struct {
	cmd *exec.Cmd
}

// Launch starts the browser with the given profile directory and returns a
// Session. The browser window opens immediately; call Session.Wait() to block
// until the user closes it.
//
// profileDir must be the full path to the profile directory (not the User Data
// dir). Chrome's --profile-directory flag expects just the directory name
// relative to User Data, so we derive both from profileDir.
//
// userDataDir is the parent "User Data" directory.
func Launch(executablePath, userDataDir, profileDirName string, extraArgs []string) (*Session, error) {
	args := []string{
		"--user-data-dir=" + userDataDir,
		"--profile-directory=" + profileDirName,
		// Prevent Chrome from stealing focus from other windows on startup
		// and suppress the "Chrome wasn't shut down correctly" bubble.
		"--no-first-run",
		"--no-default-browser-check",
	}
	args = append(args, extraArgs...)

	cmd := exec.Command(executablePath, args...)

	// Inherit the current environment so the browser can find its libraries.
	cmd.Env = os.Environ()

	// Detach stdout/stderr from our terminal — the browser has its own window.
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to launch %s: %w", executablePath, err)
	}

	fmt.Printf("  Browser PID %d started (profile: %s)\n", cmd.Process.Pid, profileDirName)
	return &Session{cmd: cmd}, nil
}

// Wait blocks until the browser process (and all its children on platforms
// that support process groups) have exited.
//
// Note: Chromium-based browsers spawn a watcher/crashpad process that outlives
// the main browser window. On Linux/macOS we use a polling loop on the process
// group; on Windows we simply wait on the main PID, which is sufficient because
// Chrome's main process is the one the user closes.
func (s *Session) Wait() error {
	_, err := s.cmd.Process.Wait()
	if err != nil {
		// On some systems the process may already have exited.
		if strings.Contains(err.Error(), "wait: no child processes") ||
			strings.Contains(err.Error(), "waitid") {
			return nil
		}
		return fmt.Errorf("waiting for browser: %w", err)
	}
	return nil
}

// Pid returns the PID of the launched browser process.
func (s *Session) Pid() int {
	if s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}
