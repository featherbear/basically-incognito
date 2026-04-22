# Chrome Profile Cloner - Plans & Known Limitations

## What the tool does

Clones the last-opened (or specified) Chrome/Chromium-based browser profile into
a new profile, carrying over:

- **Bookmarks** - `Bookmarks` JSON file
- **History** - `History` SQLite (hot-copied via `VACUUM INTO`, safe while Chrome is running)
- **Enabled extensions** - code only (`Extensions/<id>/`), filtered from `Secure Preferences`
- **Preferences** - `Preferences` + `Secure Preferences`, patched to strip identity/sync data

Intentionally excluded: cookies, saved passwords, extension storage/state, cache,
session files, sync data, shadow history (`Visited Links`), SQLite journals.

---

## Known limitations

### Chrome server-side default extensions (e.g. Rovo)
Chrome installs certain extensions for every new profile via its server-side
promo/default-extension system, independently of anything in `Preferences` or
the `Extensions/` directory. These extensions are downloaded and installed after
profile creation, making it impossible to suppress them at the filesystem level.

**Affected extensions:** Any extension with `creation_flags` indicating OEM/default
install that Chrome's component updater delivers to new profiles.

**Workaround options (not implemented):**
- Enterprise managed preferences (`/Library/Managed Preferences/` on macOS) -
  requires admin rights
- Chrome enterprise policy (`ExtensionSettings` policy) - requires MDM/admin

### Policy extensions (`location=7`)
Extensions force-installed by enterprise policy (MDM, Google Workspace Admin)
will be re-enabled by Chrome on startup regardless of the `disable_reasons` field
in `Preferences`. Chrome re-reads machine policy at startup and overrides profile
preferences for policy-managed extensions.

### Extension load warning on first launch
On the first launch of a cloned profile, some extensions using `declarativeNetRequest`
may briefly show "The extension failed to load properly." Pressing Reload on the
extension clears the error. This is a Chrome-side race condition during first-launch
initialisation of a new profile and is not specific to cloned profiles.

### Chrome running during clone
SQLite databases (`History`, `Favicons`) are hot-copied via `VACUUM INTO` with
`immutable=1`, which is safe while Chrome is running. `Local State` writes are
protected by an advisory file lock + atomic rename. However, `Bookmarks` is
copied via a plain file read - Chrome writes `Bookmarks` atomically (write to
temp + rename), so a plain read will always capture either the old or new version,
never a torn write.
