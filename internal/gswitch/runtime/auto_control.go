package runtime

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"slices"
	"strconv"
	"time"

	"github.com/arumata/gswitch/internal/gswitch/ipc"
	"github.com/arumata/gswitch/internal/gswitch/lang"
	"github.com/arumata/gswitch/internal/gswitch/layoutstate"
	"github.com/arumata/gswitch/internal/gswitch/userconfig"
)

// gswitch-auto: auto engine construction, the enable state and its
// persistence rule, hotkey suspension and the IPC controller/status. The
// daemon never writes a file: runtime changes are persisted by the tray on
// its behalf (AutoEnabledUnsaved, PendingLearnedWords; D2, C6).

// AutoUnavailableReason codes (§3.9.1).
const (
	autoUnavailableDegraded       = "degraded"
	autoUnavailableNoConverter    = "no-layout-converter"
	autoUnavailableUnsupported    = "unsupported-layouts"
	autoUnavailableLayoutSourceOf = "layout-source-off"
	autoUnavailableLayoutUnknown  = "layout-unknown"
	autoUnavailableRootMode       = "root-mode"
	autoUnavailableDetectorError  = "detector-error"
)

// forkIPCService is the part of *ipc.Service the switcher uses. Every method
// is non-blocking (§3.9.2).
type forkIPCService interface {
	UpdateStatus(st ipc.Status)
	NotifyAutoEnabled(enabled bool, source string)
	NotifyCorrected(c ipc.Correction)
	NotifyUndone(word string, learned bool)
	Close()
}

// forkIPCRef boxes the service for atomic publication (Switcher.ipcSvc;
// nil while IPC is disabled).
type forkIPCRef struct{ svc forkIPCService }

// EnableAuto builds the auto engine, or records why it is unavailable
// (autoReason) and leaves s.auto nil; manual mode is never affected.
func (s *Switcher) EnableAuto() {
	s.auto = nil
	eng, reason, err := s.buildAutoEngine()
	if reason != "" {
		s.autoReason = reason
		if err != nil {
			s.log("Automatic correction unavailable: %s (%v)", reason, err)
		} else {
			s.log("Automatic correction unavailable: %s", reason)
		}
		return
	}
	s.auto = eng
	s.autoReason = ""
	s.log("Automatic correction available: %s <-> %s",
		s.layoutConverter.Layout1.Name, s.layoutConverter.Layout2.Name)
}

func (s *Switcher) buildAutoEngine() (*AutoEngine, string, error) {
	if s.degradedMode {
		return nil, autoUnavailableDegraded, nil
	}
	lc := s.layoutConverter
	if lc == nil || lc.Layout1 == nil || lc.Layout2 == nil {
		return nil, autoUnavailableNoConverter, nil
	}
	l1, ok1 := lang.LangForLayout(lc.Layout1.Name)
	l2, ok2 := lang.LangForLayout(lc.Layout2.Name)
	if !ok1 || !ok2 || l1 == l2 {
		return nil, autoUnavailableUnsupported, nil
	}
	if s.tracker == nil {
		if forkGeteuid() == 0 {
			return nil, autoUnavailableRootMode, nil
		}
		return nil, autoUnavailableLayoutSourceOf, nil
	}
	if s.detector == nil {
		return nil, autoUnavailableDetectorError, nil
	}
	eng, err := NewAutoEngine(AutoDeps{
		Layouts:   [2]*LayoutInfo{lc.Layout1, lc.Layout2},
		Langs:     [2]lang.Lang{l1, l2},
		Judge:     s.detector,
		Layout:    s.autoLayoutSnapshot,
		Focus:     s.autoFocusSnapshot,
		Caps:      s.autoCapsLock,
		InputIdle: s.autoInputIdle,
		Emit:      s.autoEmit,
		Settings:  s.autoSettings,
		OnFix:     s.autoOnFix,
		OnUndo:    s.autoOnUndo,
		Now:       func() time.Time { return forkNow() },
		Log:       s.logDebug,
		LogWords:  s.debug && os.Getenv("GSWITCH_AUTO_LOG_WORDS") == "1",
	})
	if err != nil {
		if errors.Is(err, errAutoLangs) {
			return nil, autoUnavailableUnsupported, err
		}
		return nil, autoUnavailableDetectorError, err
	}
	return eng, "", nil
}

func (s *Switcher) autoCapsLock() (on, known bool) {
	if s.inputReader == nil {
		return false, false
	}
	return s.inputReader.CapsLockState()
}

// autoInputIdle reports an empty event queue and no key down in the kernel.
func (s *Switcher) autoInputIdle() bool {
	if len(s.eventChan) > 0 {
		return false
	}
	s.overflowMu.Lock()
	pending := len(s.overflowEvents)
	s.overflowMu.Unlock()
	if pending > 0 {
		return false
	}
	return s.inputReader == nil || !s.inputReader.AnyKeyDown()
}

// autoEmit replays an automatic fix (or its undo). The layout switch is
// always confirmed through the tracker, whatever the verify flags say (C26):
// when the shortcut is dropped nothing is typed.
func (s *Switcher) autoEmit(events []KeyEvent) error {
	st := s.userSettings()
	delay := time.Duration(st.DelayMs) * time.Millisecond
	switchDelay := time.Duration(st.SwitchDelayMs) * time.Millisecond
	if s.config != nil {
		if st.DelayMs == 0 {
			delay = time.Duration(s.config.Delay) * time.Millisecond
		}
		if st.SwitchDelayMs == 0 {
			switchDelay = time.Duration(s.config.LayoutSwitchDelay) * time.Millisecond
		}
	}
	return s.emitConversionEvents(events, emitOptions{
		reader:      trackerLayoutStateReader{tracker: s.tracker},
		delay:       delay,
		switchDelay: switchDelay,
		confirm:     true,
	})
}

// autoSettings is the engine's Settings dependency.
func (s *Switcher) autoSettings() AutoSettings {
	st := s.userSettings()
	return AutoSettings{
		Enabled:        s.autoEnabled.Load(),
		MaxLetters:     st.MaxLetters,
		Holdoff:        time.Duration(st.HoldoffMs) * time.Millisecond,
		MinInterval:    time.Duration(st.MinIntervalMs) * time.Millisecond,
		LearnOnUndo:    st.LearnOnUndo,
		LearnUndoCount: st.LearnUndoCount,
	}
}

// layoutName is the XKB layout name of a pair index ("" when unknown).
func (s *Switcher) layoutName(index int) string {
	if index >= 0 && index < len(s.layoutSpecs) {
		return s.layoutSpecs[index].Name
	}
	if lc := s.layoutConverter; lc != nil {
		switch {
		case index == 0 && lc.Layout1 != nil:
			return lc.Layout1.Name
		case index == 1 && lc.Layout2 != nil:
			return lc.Layout2.Name
		}
	}
	return ""
}

// autoOnFix records a fired fix and announces it (words blanked when
// ipc-expose-words=false).
func (s *Switcher) autoOnFix(ev FixEvent) {
	c := ipc.Correction{
		From:      ev.Shown,
		To:        ev.Alt,
		Direction: s.layoutName(ev.From) + ">" + s.layoutName(ev.To),
		Time:      ev.At,
	}
	s.lastCorrection.Store(&c)
	if ref := s.ipcSvc.Load(); ref != nil {
		ref.svc.NotifyCorrected(forkExposeCorrection(c, s.userSettings().ExposeWords))
	}
}

// autoOnUndo learns the undone word when the engine says so and announces
// the undo.
func (s *Switcher) autoOnUndo(foldedShown string, learn bool) {
	if learn {
		s.learnWord(foldedShown)
	}
	ref := s.ipcSvc.Load()
	if ref == nil {
		return
	}
	word := foldedShown
	if !s.userSettings().ExposeWords {
		word = ""
	}
	ref.svc.NotifyUndone(word, learn)
	ref.svc.UpdateStatus(s.statusSnapshot())
}

func forkExposeCorrection(c ipc.Correction, expose bool) ipc.Correction {
	if !expose {
		c.From, c.To = "", ""
	}
	return c
}

// learnWord adds a folded word to the detector's user exceptions at once and
// keeps it in pendingLearned until exceptions-words.txt contains it, so that
// the tray can persist it (PendingLearnedWords; with ipc-expose-words=false
// the word stays in daemon memory only).
func (s *Switcher) learnWord(folded string) {
	if folded == "" {
		return
	}
	if s.detector != nil {
		s.detector.AddUserException(folded)
	}
	if c := s.userCfg.Load(); c != nil && c.Words.Contains(folded) {
		return
	}
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if slices.Contains(s.pendingLearned, folded) {
		return
	}
	s.pendingLearned = append(s.pendingLearned, folded)
	if n := len(s.pendingLearned); n > forkPendingCap {
		s.pendingLearned = slices.Clone(s.pendingLearned[n-forkPendingCap:])
	}
}

// AutoEnabled reports the runtime enable state.
func (s *Switcher) AutoEnabled() bool {
	return s.autoEnabled.Load()
}

// SetAutoEnabled changes the runtime enable state. Chord and D-Bus changes
// are remembered for the echo rule of applyUserConfig and mark the state
// unsaved while it differs from auto.conf; config and startup changes are
// saved by definition. It never blocks on IPC and never writes a file.
func (s *Switcher) SetAutoEnabled(enabled bool, source string) error {
	s.autoMu.Lock()
	changed := s.setAutoEnabledLocked(enabled, source)
	s.autoMu.Unlock()
	s.announceAutoEnabled(enabled, source, changed)
	return nil
}

// setAutoEnabledLocked is the body of SetAutoEnabled; autoMu must be held. It
// reports whether the runtime value changed.
func (s *Switcher) setAutoEnabledLocked(enabled bool, source string) bool {
	changed := s.autoEnabled.Swap(enabled) != enabled
	if !enabled && s.auto != nil {
		s.enqueueControl(func() { s.auto.Cancel(forkReasonDisabled) })
	}
	switch source {
	case ipc.SourceChord, ipc.SourceDBus:
		s.lastAnnounced[forkBoolIndex(enabled)] = forkNow()
		s.autoUnsaved.Store(enabled != s.fileAutoEnabled)
	default:
		s.autoUnsaved.Store(false)
	}
	return changed
}

// announceAutoEnabled logs and publishes an enable change; autoMu must not be
// held.
func (s *Switcher) announceAutoEnabled(enabled bool, source string, changed bool) {
	if changed {
		state := "off"
		if enabled {
			state = "on"
		}
		s.log("Automatic correction %s (%s)", state, source)
	}
	ref := s.ipcSvc.Load()
	if ref == nil {
		return
	}
	if changed {
		ref.svc.NotifyAutoEnabled(enabled, source)
	}
	ref.svc.UpdateStatus(s.statusSnapshot())
}

func forkBoolIndex(b bool) int {
	if b {
		return 1
	}
	return 0
}

// SuspendHotkeys suspends (tray key picker open) or resumes gestures and
// chords. While suspended every key event is consumed before the converter;
// on resume the next word restarts tainted until a boundary, because the
// keys typed into the capture dialog were consumed.
func (s *Switcher) SuspendHotkeys(suspend bool) {
	s.hotkeysSuspended.Store(suspend)
	if suspend {
		s.enqueueControl(func() {
			s.converter.ClearBuffer()
			if s.auto != nil {
				s.auto.Cancel(forkReasonSuspended)
			}
		})
	} else {
		s.enqueueControl(func() {
			s.converter.ClearBuffer()
			s.afterInputDiscarded()
		})
	}
	s.pushStatus()
}

// ipcController implements ipc.Controller. Its methods may call the Service
// synchronously (§3.9.2).
type ipcController struct{ s *Switcher }

// AutoEnabled implements ipc.Controller.
func (c ipcController) AutoEnabled() bool { return c.s.AutoEnabled() }

// SetAutoEnabled implements ipc.Controller (D-Bus method and property Set).
func (c ipcController) SetAutoEnabled(enabled bool, source string) error {
	return c.s.SetAutoEnabled(enabled, source)
}

// Reload implements ipc.Controller.
func (c ipcController) Reload() error { return c.s.ReloadUserConfig() }

// SuspendHotkeys implements ipc.Controller; the Service tracks the owner.
func (c ipcController) SuspendHotkeys(suspend bool, owner string) error {
	c.s.logDebug("hotkeys suspended=%v by %s", suspend, owner)
	c.s.SuspendHotkeys(suspend)
	return nil
}

// Snapshot implements ipc.Controller.
func (c ipcController) Snapshot() ipc.Status { return c.s.statusSnapshot() }

// pushStatus publishes the current status (non-blocking).
func (s *Switcher) pushStatus() {
	if ref := s.ipcSvc.Load(); ref != nil {
		ref.svc.UpdateStatus(s.statusSnapshot())
	}
}

// statusLoop republishes the status once per second when it changed, so that
// Stats and CorrectionCount never cause per-keystroke traffic.
func (s *Switcher) statusLoop(ctx context.Context) {
	defer s.forkWG.Done()
	ticker := time.NewTicker(forkStatusInterval)
	defer ticker.Stop()
	var last ipc.Status
	published := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		ref := s.ipcSvc.Load()
		if ref == nil {
			continue
		}
		st := s.statusSnapshot()
		if published && reflect.DeepEqual(st, last) {
			continue
		}
		ref.svc.UpdateStatus(st)
		last, published = st, true
	}
}

// autoAvailability maps the engine and tracker state to AutoAvailable and
// AutoUnavailableReason. Pending counts as available: it lasts at most
// layout-hint-timeout-ms and must not make the tray flicker.
func (s *Switcher) autoAvailability(ls layoutstate.State) (bool, string) {
	if s.auto == nil {
		return false, s.autoReason
	}
	switch ls.Confidence {
	case layoutstate.Confirmed, layoutstate.Pending:
		return true, ""
	default:
		return false, autoUnavailableLayoutUnknown
	}
}

// statusSnapshot builds the IPC status. It runs on any goroutine and only
// reads atomics and mutex-guarded state.
func (s *Switcher) statusSnapshot() ipc.Status {
	cfg := s.userCfg.Load()
	settings := s.userSettings()
	st := ipc.Status{
		AutoEnabled:         s.autoEnabled.Load(),
		AutoEnabledUnsaved:  s.autoUnsaved.Load(),
		HotkeysSuspended:    s.hotkeysSuspended.Load(),
		ConfigDir:           s.userDir,
		Version:             version,
		PendingLearnedWords: []string{},
	}

	ls := s.tracker.State()
	st.AutoAvailable, st.UnavailableReason = s.autoAvailability(ls)
	if ls.Index >= 0 {
		st.ActiveLayout = s.layoutName(ls.Index)
	}
	st.LayoutKnown = ls.Confidence == layoutstate.Confirmed && ls.Index >= 0
	st.LayoutSource = ls.Source

	fs := s.focusState()
	st.FocusSource = s.focusSourceName(fs)
	if s.focusClient != nil {
		st.FocusedApp, st.FocusedWmClass, st.InputPurpose = fs.AppID, fs.WmClass, string(fs.Purpose)
	}
	allowed, reason := forkFocusDecision(fs, s.focusPolicy.Load())
	st.FocusSuppressed, st.FocusSuppressedReason = !allowed, reason

	toggle, selection, chordWarnings := applyChordRuntimeChecks(settings, s.layoutSwitchKeys(), s.convertKey())
	st.ToggleChord, st.SelectionChord = toggle.String(), selection.String()
	st.ConfigWarnings = s.configWarnings(cfg, chordWarnings)

	if c := s.lastCorrection.Load(); c != nil {
		st.Last = forkExposeCorrection(*c, settings.ExposeWords)
	}
	stats := s.auto.Stats()
	st.Count = math.MaxUint32
	if stats.Fired < math.MaxUint32 {
		st.Count = uint32(stats.Fired)
	}
	st.Stats = map[string]uint64{
		"armed":       stats.Armed,
		"fired":       stats.Fired,
		"cancelled":   stats.Cancelled,
		"skipped":     stats.Skipped,
		"undone":      stats.Undone,
		"learned":     stats.Learned,
		"garble_risk": stats.GarbleRisk,
	}

	if settings.ExposeWords {
		s.pendingMu.Lock()
		st.PendingLearnedWords = append(st.PendingLearnedWords, s.pendingLearned...)
		s.pendingMu.Unlock()
	}
	return st
}

// configWarnings lists the loader warnings, the runtime chord warnings and
// the keys that only apply after a restart.
func (s *Switcher) configWarnings(cfg *userconfig.Config, chordWarnings []string) []string {
	warnings := []string{}
	if cfg != nil {
		warnings = append(warnings, cfg.Warnings...)
	}
	warnings = append(warnings, chordWarnings...)
	if cfg != nil {
		warnings = append(warnings, s.restartWarnings(cfg.Settings)...)
	}
	return warnings
}

// restartWarnings names the restart-only keys (focus-source, layout-source,
// layout-hint-timeout-ms) whose configured value differs from the value the
// daemon started with. It is empty before initFork.
func (s *Switcher) restartWarnings(now userconfig.Settings) []string {
	run := s.startSettings
	if run.LayoutSource == "" {
		return nil
	}
	var warnings []string
	restart := func(key, running, configured string) {
		if running != configured {
			warnings = append(warnings, fmt.Sprintf("%s=%s takes effect after a restart (running with %s)",
				key, configured, running))
		}
	}
	restart(userconfig.KeyFocusSource, run.FocusSource, now.FocusSource)
	restart(userconfig.KeyLayoutSource, run.LayoutSource, now.LayoutSource)
	restart(userconfig.KeyLayoutHintTimeoutMs,
		strconv.Itoa(run.LayoutHintTimeoutMs), strconv.Itoa(now.LayoutHintTimeoutMs))
	return warnings
}

func (s *Switcher) layoutSwitchKeys() []uint16 {
	if s.converter == nil {
		return nil
	}
	return s.converter.LSKeys
}

func (s *Switcher) convertKey() uint16 {
	if s.config == nil {
		return 0
	}
	return s.config.ConvertKey
}
