package userconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// ErrListFull is returned when a list write would exceed MaxListEntries
// entries or MaxListBytes bytes.
var ErrListFull = errors.New("exception list is full")

// renameFile is os.Rename; tests replace it to simulate a failed rename.
var renameFile = os.Rename

// ensureDir creates dir (and missing parents) with mode 0700 and forces 0700
// on an existing directory (upstream's tray creates it 0750).
func ensureDir(dir string) error {
	if err := checkDir(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("userconfig: create %s: %w", dir, err)
	}
	if err := os.Chmod(dir, dirMode); err != nil {
		return fmt.Errorf("userconfig: chmod %s: %w", dir, err)
	}
	return nil
}

// withLock runs fn while holding an exclusive flock on <dir>/.lock. The lock
// serialises every writer (also writers in one process: each call opens its
// own file description).
func withLock(dir string, fn func() error) error {
	if err := ensureDir(dir); err != nil {
		return err
	}
	lockPath := filepath.Join(dir, lockName)
	f, err := os.OpenFile(filepath.Clean(lockPath), os.O_RDWR|os.O_CREATE, fileMode)
	if err != nil {
		return fmt.Errorf("userconfig: open lock: %w", err)
	}
	defer f.Close()
	fd := int(f.Fd()) //nolint:gosec // file descriptors fit in int
	for {
		err = unix.Flock(fd, unix.LOCK_EX)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("userconfig: lock %s: %w", lockPath, err)
	}
	defer func() { _ = unix.Flock(fd, unix.LOCK_UN) }()
	return fn()
}

// writeAtomic writes data to a temporary file in the target's directory,
// syncs it and renames it over the target; the result has mode 0600. On any
// failure the temporary file is removed and the target is left untouched.
// A target that is a symlink (dotfiles managers such as stow) is written
// through: the file it resolves to is replaced in its own directory and the
// link is kept. A dangling link is replaced by a regular file.
func writeAtomic(path, data string) (err error) {
	if resolved, rerr := filepath.EvalSymlinks(path); rerr == nil {
		path = resolved
	}
	dir, base := filepath.Split(path)
	tmp, err := os.CreateTemp(dir, "."+base+".*.tmp")
	if err != nil {
		return fmt.Errorf("userconfig: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()
	if err = tmp.Chmod(fileMode); err != nil {
		return fmt.Errorf("userconfig: chmod temp file: %w", err)
	}
	if _, err = tmp.WriteString(data); err != nil {
		return fmt.Errorf("userconfig: write %s: %w", path, err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("userconfig: sync %s: %w", path, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("userconfig: close %s: %w", path, err)
	}
	if err = renameFile(tmpPath, path); err != nil {
		return fmt.Errorf("userconfig: rename %s: %w", path, err)
	}
	return nil
}

// readForUpdate returns the current text of a file for a read-modify-write,
// or its template (exists=false) when the file does not exist. A file larger
// than limit is an error: it is never rewritten.
func readForUpdate(dir, name string, limit int64) (text string, exists bool, err error) {
	text, exists, err = readCapped(filepath.Join(dir, name), limit)
	if err != nil {
		return "", false, err
	}
	if !exists {
		return templateFor(name), false, nil
	}
	if int64(len(text)) > limit {
		return "", true, fmt.Errorf("userconfig: %s is larger than %d bytes; not rewritten", name, limit)
	}
	return text, true, nil
}

// joinLines joins lines with LF and a final LF (empty input gives "").
func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// Save writes every value of s to auto.conf (creating it from the template
// when missing) with the key-preserving rules of Update. s must be valid.
func Save(dir string, s Settings) error {
	if err := s.Validate(); err != nil {
		return fmt.Errorf("userconfig: %w", err)
	}
	return Update(dir, s.Values())
}

// SetAutoEnabled writes only the auto-enabled key.
func SetAutoEnabled(dir string, enabled bool) error {
	return Update(dir, map[string]string{KeyAutoEnabled: strconv.FormatBool(enabled)})
}

// Update sets the given auto.conf keys under the directory lock. Every key
// must be an auto.conf key and every value must be valid (nothing is written
// otherwise). Every existing assignment of a key is rewritten in place;
// a key without an assignment is inserted right after its commented template
// line ("# key=default") when there is one, else appended. Comments, unknown
// lines and the order of all other lines are kept. A missing file is created
// from the template. The file is not rewritten when nothing changes.
func Update(dir string, values map[string]string) error {
	canonical, err := canonicalValues(values)
	if err != nil {
		return err
	}
	if err := checkDir(dir); err != nil {
		return err
	}
	if len(canonical) == 0 {
		return nil
	}
	return withLock(dir, func() error {
		old, exists, err := readForUpdate(dir, AutoConfName, MaxAutoConfBytes)
		if err != nil {
			return err
		}
		updated := updateAutoConf(old, canonical)
		if exists && updated == old {
			return nil
		}
		return writeAtomic(filepath.Join(dir, AutoConfName), updated)
	})
}

// assignmentKey returns the key of an auto.conf assignment line.
func assignmentKey(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "[") {
		return "", false
	}
	key, _, ok := strings.Cut(t, "=")
	return strings.TrimSpace(key), ok
}

// commentedKey returns the key of a commented-out assignment ("# key=value"
// with a single-token value, as in the template).
func commentedKey(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "#") {
		return "", false
	}
	key, value, ok := strings.Cut(strings.TrimSpace(strings.TrimLeft(t, "#")), "=")
	if !ok || key == "" || strings.ContainsAny(key, " \t") || strings.ContainsAny(value, " \t") {
		return "", false
	}
	return key, true
}

func updateAutoConf(text string, values map[string]string) string {
	lines := splitLines(text)
	done := make(map[string]bool, len(values))
	for i, line := range lines {
		if key, ok := assignmentKey(line); ok {
			if v, set := values[key]; set {
				lines[i] = key + "=" + v
				done[key] = true
			}
		}
	}
	var appended []string
	for _, key := range Keys() {
		v, set := values[key]
		if !set || done[key] {
			continue
		}
		line := key + "=" + v
		inserted := false
		for i, l := range lines {
			if ck, ok := commentedKey(l); ok && ck == key {
				lines = slices.Insert(lines, i+1, line)
				inserted = true
				break
			}
		}
		if !inserted {
			appended = append(appended, line)
		}
	}
	return joinLines(append(lines, appended...))
}

// EnsureFiles creates the directory (0700) and seeds every missing file
// (auto.conf and both lists) from its template. Existing files are kept.
func EnsureFiles(dir string) error {
	return withLock(dir, func() error {
		for _, name := range watchedNames {
			path := filepath.Join(dir, name)
			if _, err := os.Lstat(path); err == nil {
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("userconfig: %w", err)
			}
			if err := writeAtomic(path, templateFor(name)); err != nil {
				return err
			}
		}
		return nil
	})
}

// ReadListText returns the raw text of an exception list ("" when missing).
func ReadListText(dir, name string) (string, error) {
	if _, err := listSpecFor(name); err != nil {
		return "", err
	}
	if err := checkDir(dir); err != nil {
		return "", err
	}
	text, _, err := readCapped(filepath.Join(dir, name), MaxListBytes)
	if err != nil {
		return "", err
	}
	if len(text) > MaxListBytes {
		return "", fmt.Errorf("userconfig: %s is larger than %d bytes", name, MaxListBytes)
	}
	return text, nil
}

// WriteListText replaces an exception list with text verbatim (templates and
// tests; the settings window uses ApplyListDiff).
func WriteListText(dir, name, text string) error {
	if _, err := listSpecFor(name); err != nil {
		return err
	}
	if len(text) > MaxListBytes {
		return fmt.Errorf("userconfig: %w: %s would be larger than %d bytes", ErrListFull, name, MaxListBytes)
	}
	return withLock(dir, func() error {
		return writeAtomic(filepath.Join(dir, name), text)
	})
}

// AppendListEntry normalises entry and appends it to the list unless it is
// already present (added=false). A missing file is created from the template.
func AppendListEntry(dir, name, entry string) (added bool, err error) {
	spec, err := listSpecFor(name)
	if err != nil {
		return false, err
	}
	normalized, ok := spec.normalize(entry)
	if !ok {
		return false, fmt.Errorf("userconfig: invalid %s entry %q", name, entry)
	}
	err = editList(dir, spec, func(lines []string, exists bool) ([]string, bool) {
		if !containsEntry(spec, lines, normalized) {
			lines = append(lines, normalized)
		} else if exists {
			return lines, false
		}
		// A missing file is created from the template, which may already
		// list the entry; either way the entry is new on disk.
		added = true
		return lines, true
	})
	if err != nil {
		return false, err
	}
	return added, nil
}

// RemoveListEntry removes every line whose entry normalises to entry.
// Comments and other lines are kept; a missing file or entry is not an error.
func RemoveListEntry(dir, name, entry string) error {
	return ApplyListDiff(dir, name, nil, []string{entry})
}

// ApplyListDiff re-reads the list under one flock, removes the lines whose
// entry is in remove, appends the entries of add that are missing
// (normalised, deduplicated, in the given order), keeps comments and the
// order of untouched lines, and writes atomically. Entries appended
// concurrently after the caller loaded its snapshot are preserved. An invalid
// add entry fails the whole call; invalid remove entries match nothing.
func ApplyListDiff(dir, name string, add, remove []string) error {
	spec, err := listSpecFor(name)
	if err != nil {
		return err
	}
	addNorm := make([]string, 0, len(add))
	for _, a := range add {
		n, ok := spec.normalize(a)
		if !ok {
			return fmt.Errorf("userconfig: invalid %s entry %q", name, a)
		}
		addNorm = append(addNorm, n)
	}
	removeSet := make(map[string]bool, len(remove))
	for _, r := range remove {
		if n, ok := spec.normalize(r); ok {
			removeSet[n] = true
		}
	}
	if len(addNorm) == 0 && len(removeSet) == 0 {
		return nil
	}
	return editList(dir, spec, func(lines []string, exists bool) ([]string, bool) {
		// A missing file is created (from the template) only when something
		// is added; removing from a missing file changes nothing.
		if !exists && len(addNorm) == 0 {
			return lines, false
		}
		changed := !exists
		kept := lines[:0]
		present := make(map[string]bool, len(lines))
		for _, line := range lines {
			if entry, ok := normalizedLineEntry(spec, line); ok {
				if removeSet[entry] {
					changed = true
					continue
				}
				present[entry] = true
			}
			kept = append(kept, line)
		}
		for _, a := range addNorm {
			if !present[a] {
				kept = append(kept, a)
				present[a] = true
				changed = true
			}
		}
		return kept, changed
	})
}

// normalizedLineEntry returns the normalised entry of a list line.
func normalizedLineEntry(spec listSpec, line string) (string, bool) {
	raw, ok := listLineEntry(line)
	if !ok {
		return "", false
	}
	return spec.normalize(raw)
}

// containsEntry reports whether a line of lines normalises to entry.
func containsEntry(spec listSpec, lines []string, entry string) bool {
	for _, line := range lines {
		if e, ok := normalizedLineEntry(spec, line); ok && e == entry {
			return true
		}
	}
	return false
}

// editList runs a read-modify-write of a list under the directory lock. edit
// receives the current lines (the template's when the file does not exist)
// and returns the new lines and whether anything changed; nothing is written
// without a change. A result that grows beyond MaxListBytes or
// MaxListEntries fails with ErrListFull.
func editList(dir string, spec listSpec, edit func(lines []string, exists bool) ([]string, bool)) error {
	if err := checkDir(dir); err != nil {
		return err
	}
	return withLock(dir, func() error {
		text, exists, err := readForUpdate(dir, spec.name, MaxListBytes)
		if err != nil {
			return err
		}
		lines := splitLines(text)
		before := countEntries(spec, lines)
		lines, changed := edit(lines, exists)
		if !changed {
			return nil
		}
		out := joinLines(lines)
		if len(out) > MaxListBytes {
			return fmt.Errorf("userconfig: %w: %s would be larger than %d bytes", ErrListFull, spec.name, MaxListBytes)
		}
		if after := countEntries(spec, lines); after > MaxListEntries && after > before {
			return fmt.Errorf("userconfig: %w: %s would have more than %d entries", ErrListFull, spec.name, MaxListEntries)
		}
		return writeAtomic(filepath.Join(dir, spec.name), out)
	})
}

// countEntries counts the distinct valid entries of lines.
func countEntries(spec listSpec, lines []string) int {
	seen := make(map[string]bool, len(lines))
	for _, line := range lines {
		if entry, ok := normalizedLineEntry(spec, line); ok {
			seen[entry] = true
		}
	}
	return len(seen)
}
