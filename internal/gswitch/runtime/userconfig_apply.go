package runtime

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/arumata/gswitch/internal/gswitch/ipc"
	"github.com/arumata/gswitch/internal/gswitch/lang"
	"github.com/arumata/gswitch/internal/gswitch/userconfig"
)

// gswitch-auto: loading and hot-applying ~/.config/gswitch. The daemon only
// reads the directory (D2): under the upstream unit every write under $HOME
// fails, and the tray is the only programmatic writer.

var errForkNoUserDir = errors.New("user configuration directory unknown")

// loadUserConfig resolves the user configuration directory (from the session
// environment in root compatibility mode) and loads it read-only. It never
// fails: errors become warnings and defaults (with auto-enabled=false).
func (s *Switcher) loadUserConfig() *userconfig.Config {
	dir, err := s.resolveUserDir()
	if err != nil {
		s.log("Warning: user config directory: %v", err)
		dir = ""
	}
	s.userDir = dir
	c, _ := userconfig.Load(dir) // never nil; an error is also a warning
	for _, w := range c.Warnings {
		s.log("Warning: user config: %s", w)
	}
	s.userCfg.Store(c)
	return c
}

func (s *Switcher) resolveUserDir() (string, error) {
	if s.sessionEnv != nil {
		return userconfig.DirFor(s.sessionEnv.Home, s.sessionEnv.XDGConfigHome)
	}
	return userconfig.Dir()
}

// userSettings returns the current user settings (defaults before initFork).
func (s *Switcher) userSettings() userconfig.Settings {
	if c := s.userCfg.Load(); c != nil {
		return c.Settings
	}
	return userconfig.DefaultSettings()
}

// forkDetectorOptions maps the user settings to detector options.
func forkDetectorOptions(st userconfig.Settings) lang.Options {
	return lang.Options{
		MinLetters: st.MinLetters,
		MinMargin:  st.Margin,
		MaxAltCost: st.MaxAltCost,
		ShortWords: st.ShortWords,
	}
}

// initDetector builds the language detector with the user's options and
// exception words; invalid options fall back to the defaults.
func (s *Switcher) initDetector(c *userconfig.Config) {
	d, err := lang.NewDetector(forkDetectorOptions(c.Settings))
	if err != nil {
		s.log("Warning: detector options: %v; using defaults", err)
		d, err = lang.NewDetector(lang.DefaultOptions())
	}
	if err != nil {
		s.logError("language detector unavailable: %v", err)
		return
	}
	d.SetUserExceptions(c.Words.Entries())
	s.detector = d
}

// watchUserConfig follows the configuration directory until ctx is done.
// The configuration applied so far is the watcher's baseline, so a change
// made between initFork's Load and the start of the watch (for example the
// tray seeding exceptions-apps.txt at the first login) is applied too.
func (s *Switcher) watchUserConfig(ctx context.Context) {
	defer s.forkWG.Done()
	opts := userconfig.Options{Baseline: s.userCfg.Load()}
	if err := userconfig.Watch(ctx, s.userDir, s.applyUserConfig, opts); err != nil {
		s.log("Warning: user config watcher: %v", err)
	}
}

// ReloadUserConfig loads the directory and applies it (D-Bus Reload). A load
// error is returned after the usable part has been applied.
func (s *Switcher) ReloadUserConfig() error {
	if s.userDir == "" {
		return errForkNoUserDir
	}
	c, err := userconfig.Load(s.userDir)
	s.applyUserConfig(c)
	return err
}

// applyChordRuntimeChecks returns the chords that can run with the current
// layout-switch keys and convert key: a chord that contains every
// layout-switch key or the convert key is disabled with a warning (never a
// startup failure), and the selection chord is disabled when it overlaps the
// toggle chord.
func applyChordRuntimeChecks(st userconfig.Settings, lsKeys []uint16, convertKey uint16) (
	toggle, selection userconfig.Chord, warnings []string,
) {
	check := func(key string, c userconfig.Chord) userconfig.Chord {
		switch {
		case len(c) == 0:
			return nil
		case len(lsKeys) > 0 && c.ContainsAll(lsKeys):
			warnings = append(warnings, fmt.Sprintf("%s=%s contains the layout switch keys %s; chord disabled",
				key, c, userconfig.Chord(lsKeys)))
			return nil
		case convertKey != 0 && slices.Contains(c, convertKey):
			warnings = append(warnings, fmt.Sprintf("%s=%s contains the convert key %d; chord disabled",
				key, c, convertKey))
			return nil
		}
		return c
	}
	toggle = check(userconfig.KeyToggleChord, st.ToggleChord)
	selection = check(userconfig.KeySelectionChord, st.SelectionChord)
	if toggle.Overlaps(selection) {
		warnings = append(warnings, fmt.Sprintf("%s=%s overlaps %s=%s; chord disabled",
			userconfig.KeySelectionChord, selection, userconfig.KeyToggleChord, toggle))
		selection = nil
	}
	return toggle, selection, warnings
}

// applyChords installs the effective chords of st when they differ from
// those of prev (nil: initial install). A reload that changes the chords
// disarms the auto engine's undo.
func (s *Switcher) applyChords(prev *userconfig.Config, st userconfig.Settings) {
	toggle, selection, warnings := applyChordRuntimeChecks(st, s.layoutSwitchKeys(), s.convertKey())
	if prev != nil {
		oldToggle, oldSelection, _ := applyChordRuntimeChecks(prev.Settings, s.layoutSwitchKeys(), s.convertKey())
		if toggle.Equal(oldToggle) && selection.Equal(oldSelection) {
			return
		}
	}
	for _, w := range warnings {
		s.log("Warning: user config: %s", w)
	}
	s.chords.Set(map[ChordAction][]uint16{
		ChordToggleAuto:       toggle,
		ChordConvertSelection: selection,
	})
	s.logDebug("chords: toggle=%s selection=%s", toggle, selection)
	if prev != nil && s.auto != nil {
		s.enqueueControl(func() { s.auto.Cancel(forkReasonReload) })
	}
}

// applyUserConfig swaps in a newly loaded configuration (watcher goroutine or
// D-Bus Reload): detector options and exceptions (file words plus words
// learned but not yet persisted), chords, focus policy, pending learned words,
// and the auto-enabled rule of §3.10.5. Notifications are sent after autoMu is
// released.
func (s *Switcher) applyUserConfig(c *userconfig.Config) {
	if c == nil {
		return
	}
	s.autoMu.Lock()
	prev := s.userCfg.Swap(c)
	if s.detector != nil {
		if err := s.detector.SetOptions(forkDetectorOptions(c.Settings)); err != nil {
			s.log("Warning: detector options: %v", err)
		}
	}
	s.syncLearnedExceptions(c)
	s.applyChords(prev, c.Settings)
	s.focusPolicy.Store(forkFocusPolicy(c))
	changed, enabled := s.applyAutoEnabledRuleLocked(c.Settings.AutoEnabled)
	s.autoMu.Unlock()

	s.logNewWarnings(prev, c)
	if changed {
		s.announceAutoEnabled(enabled, ipc.SourceConfig, true)
		return
	}
	s.pushStatus()
}

// applyAutoEnabledRuleLocked applies the file value f of auto-enabled (autoMu
// held). It reports whether the runtime value changed (source "config").
func (s *Switcher) applyAutoEnabledRuleLocked(f bool) (changed, enabled bool) {
	r := s.autoEnabled.Load()
	s.fileAutoEnabled = f
	switch {
	case f == r:
		s.autoUnsaved.Store(false)
		return false, r
	case s.autoUnsaved.Load() || forkNow().Sub(s.lastAnnounced[forkBoolIndex(f)]) < forkEchoWindow:
		// A stale file or a late echo of an earlier toggle (on->off within a
		// second: the tray's write of "true" lands after the daemon is off).
		// Keep the runtime value; the tray writes it.
		s.autoUnsaved.Store(true)
		return false, r
	default:
		// A genuine external edit (hand edit, offline tray toggle).
		return s.setAutoEnabledLocked(f, ipc.SourceConfig), f
	}
}

// syncLearnedExceptions drops every learned word that the file now contains
// and rebuilds the detector's user exceptions from the file words plus the
// remaining learned ones. pendingMu is held across both steps so that a word
// learned concurrently (learnWord) is never lost from the detector.
func (s *Switcher) syncLearnedExceptions(c *userconfig.Config) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	inFile := func(w string) bool { return c.Words.Contains(w) }
	s.pendingLearned = slices.DeleteFunc(s.pendingLearned, inFile)
	s.memoryLearned = slices.DeleteFunc(s.memoryLearned, inFile)
	if s.detector != nil {
		words := append(c.Words.Entries(), s.pendingLearned...)
		s.detector.SetUserExceptions(append(words, s.memoryLearned...))
	}
}

// logNewWarnings logs loader and restart-only warnings that the previous
// configuration did not have.
func (s *Switcher) logNewWarnings(prev, c *userconfig.Config) {
	var old []string
	if prev != nil {
		old = append(slices.Clone(prev.Warnings), s.restartWarnings(prev.Settings)...)
	}
	for _, w := range append(slices.Clone(c.Warnings), s.restartWarnings(c.Settings)...) {
		if !slices.Contains(old, w) {
			s.log("Warning: user config: %s", w)
		}
	}
}
