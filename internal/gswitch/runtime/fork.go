package runtime

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/arumata/gswitch/internal/gswitch/focus"
	"github.com/arumata/gswitch/internal/gswitch/ipc"
	"github.com/arumata/gswitch/internal/gswitch/lang"
	"github.com/arumata/gswitch/internal/gswitch/layoutstate"
	"github.com/arumata/gswitch/internal/gswitch/userconfig"
)

// gswitch-auto integration (WP11). switcher.go calls the methods of this file
// through one-line hooks; every hook is nil-safe, so a Switcher built as a
// struct literal (tests) or without a layout tracker (root mode, X11/KDE,
// layout-source=none) runs exactly the upstream code.

const (
	// forkControlQueue is the capacity of controlChan.
	forkControlQueue = 32
	// forkStatusInterval is the period of statusLoop (Stats and
	// CorrectionCount are refreshed at most this often).
	forkStatusInterval = time.Second
	// forkRestoreWait bounds the wait for a late IBus signal after a
	// selection chord whose press order could fire the GNOME layout binding.
	forkRestoreWait = 150 * time.Millisecond
	// forkRestorePoll is the tracker poll period during forkRestoreWait.
	forkRestorePoll = 10 * time.Millisecond
	// forkEchoWindow is how long a file value equal to an earlier chord/D-Bus
	// toggle is treated as a late echo of that toggle (§3.10.5).
	forkEchoWindow = 5 * time.Second
	// forkPendingCap bounds pendingLearned (oldest dropped).
	forkPendingCap = 64
)

// Engine cancellation reasons owned by the integration layer.
const (
	forkReasonChord     = "chord"
	forkReasonManual    = "manual"
	forkReasonFocus     = "focus"
	forkReasonDisabled  = "disabled"
	forkReasonSuspended = "suspended"
	forkReasonReload    = "reload"
)

// Test hooks. Production values are the real system calls.
var (
	// forkGeteuid decides root compatibility mode for every fork component.
	forkGeteuid = os.Geteuid
	// forkNow is the clock of the auto engine and of the persistence rule.
	forkNow = time.Now
	// forkSleep paces the late-signal wait of restoreLayoutIfChordFlipped.
	forkSleep = time.Sleep
	// forkKeysDown queries the kernel key state after discarded input.
	forkKeysDown = func(ir *InputReader) (map[uint16]bool, error) { return ir.KeysDown() }
)

var errForkNoInputReader = errors.New("no input reader")

// forkState holds the fork's Switcher fields (§3.10.1). It is embedded in
// Switcher so that the upstream struct gains a single line; the zero value
// disables every fork feature.
type forkState struct {
	layoutSpecs []LayoutSpec // resolved pair (set in initLayoutConverter)
	controlChan chan func()  // cap forkControlQueue; nil before initFork
	forkWG      sync.WaitGroup
	forkCancel  context.CancelFunc // cancels the fork goroutines started by startFork

	userDir       string
	userCfg       atomic.Pointer[userconfig.Config] // read-only view; the daemon never writes (D2)
	startSettings userconfig.Settings               // settings at start, for the restart-only keys

	autoMu          sync.Mutex   // guards the auto-enabled transition rule (§3.10.5); never held while calling IPC
	fileAutoEnabled bool         // auto-enabled as last loaded from auto.conf (autoMu)
	autoUnsaved     atomic.Bool  // runtime value changed by chord/dbus, not yet seen on disk
	lastAnnounced   [2]time.Time // [false],[true]: last chord/dbus SetAutoEnabled of that value (autoMu)

	// pendingMu serialises every change of the detector's user exceptions
	// (learnWord, applyUserConfig) with the two learned-word lists, so that a
	// reload never drops a word learned concurrently.
	pendingMu      sync.Mutex
	pendingLearned []string // learned folded words not yet in exceptions-words.txt (cap forkPendingCap)
	memoryLearned  []string // words learned while ipc-expose-words=false: detector only, never published (cap forkPendingCap)

	tracker    *layoutstate.Tracker // nil: layout-source=none, non-GNOME session, root mode, no layout pair
	lsKeysDown map[uint16]bool      // layout-switch keys physically down (Run goroutine only)

	focusClient *focus.Client // nil when focus-source=off or root
	focusPolicy atomic.Pointer[focus.Policy]

	detector    *lang.Detector
	auto        *AutoEngine // nil => auto unavailable (reason in autoReason)
	autoReason  string
	autoEnabled atomic.Bool

	chords           ChordDetector     // value type, zero = pass-through
	chordStartLayout layoutstate.State // zero when tracker == nil
	chordSawAltShift bool              // a Shift went down while an Alt was held (or LAlt while a Shift was) in this chord cycle
	forkAltDown      uint8             // bit0 = LAlt(56), bit1 = RAlt(100) physically down (Run goroutine only)
	hotkeysSuspended atomic.Bool
	expectOwnGen     uint64 // tracker generations <= this are our own switches (Run goroutine only)

	ipcSvc         atomic.Pointer[forkIPCRef] // nil when IPC is disabled
	lastCorrection atomic.Pointer[ipc.Correction]
}

// initFork runs at the end of NewSwitcher. It reads the user configuration
// (read-only), builds the detector, the layout tracker, the focus client and
// the auto engine, but starts no goroutine. It never fails: every missing
// dependency only leaves automatic correction unavailable.
func (s *Switcher) initFork() {
	s.controlChan = make(chan func(), forkControlQueue)
	cfg := s.loadUserConfig()
	s.startSettings = cfg.Settings
	s.focusPolicy.Store(forkFocusPolicy(cfg))
	s.applyChords(nil, cfg.Settings)
	s.initDetector(cfg)
	s.initTracker(cfg.Settings)
	if s.tracker != nil {
		// Only with a tracker: without one the manual path keeps the
		// upstream gsettings reader and never touches tracker state.
		s.layoutReaderFn = newCompositeLayoutReader(s.tracker, s.sessionEnv)
		s.onOwnSwitch = s.noteOwnSwitch
	}
	s.initFocusClient(cfg.Settings)
	s.EnableAuto()
	s.autoEnabled.Store(cfg.Settings.AutoEnabled)
	s.fileAutoEnabled = cfg.Settings.AutoEnabled
}

// startFork runs in Run() once the event channel exists: it starts the
// tracker, the focus client, the user-config watcher, the status loop and the
// IPC service. A failing IPC start is logged and the daemon continues.
func (s *Switcher) startFork() {
	parent := s.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	s.forkCancel = cancel

	if s.tracker != nil {
		s.tracker.Start(ctx)
	}
	if s.focusClient != nil {
		s.focusClient.Start(ctx)
	}
	if s.userDir != "" {
		s.forkWG.Add(1)
		go s.watchUserConfig(ctx)
	}
	s.forkWG.Add(1)
	go s.statusLoop(ctx)

	svc, err := ipc.Start(ctx, ipcController{s: s}, s.logDebug)
	if err != nil {
		s.log("%v", err)
		return
	}
	s.ipcSvc.Store(&forkIPCRef{svc: svc})
	s.pushStatus()
}

// stopFork stops everything startFork started. It is idempotent and safe on
// a Switcher that never ran.
func (s *Switcher) stopFork() {
	if s.forkCancel != nil {
		s.forkCancel()
	}
	if ref := s.ipcSvc.Swap(nil); ref != nil {
		ref.svc.Close()
	}
	s.tracker.Stop()
	if s.focusClient != nil {
		s.focusClient.Close()
	}
	done := make(chan struct{})
	go func() {
		s.forkWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(ShutdownTimeoutMs * time.Millisecond):
		s.logDebug("Timeout waiting for fork goroutines.")
	}
}

// enqueueControl runs fn on the Run goroutine. It never blocks; it returns
// false (and fn is dropped) when the queue is full or the fork is not
// initialised. Correctness never depends on delivery: the engine re-reads the
// layout and focus state at decision and fire time.
func (s *Switcher) enqueueControl(fn func()) bool {
	if fn == nil || s.controlChan == nil {
		return false
	}
	select {
	case s.controlChan <- fn:
		return true
	default:
		s.logDebug("control queue full, notification dropped")
		return false
	}
}

// loopWait is the Run loop timeout: EventLoopTimeoutMs, or less while an
// automatic fix waits for its deadline.
func (s *Switcher) loopWait() time.Duration {
	wait := EventLoopTimeoutMs * time.Millisecond
	if d, ok := s.auto.NextDeadline(forkNow()); ok && d < wait {
		wait = max(d, time.Millisecond)
	}
	return wait
}

// autoTick lets the auto engine fire a due fix after each loop iteration.
func (s *Switcher) autoTick() {
	if s.auto != nil && !s.inConversion.Load() {
		s.auto.Tick(s.converter)
	}
}

// forkPreProcess runs right after trackSelectionModifiers. It returns true
// when the event is consumed (hotkey suspension, fired chord).
func (s *Switcher) forkPreProcess(event *InputEvent) bool {
	s.observeLayoutSwitchChord(event)
	if s.auto != nil {
		s.auto.Observe(event.Code, event.Value, s.ctrlPressed.Load())
	}
	if s.hotkeysSuspended.Load() {
		// The tray's key picker is open: no gestures, no chords, no buffer.
		return true
	}
	if event.Value == K_DOWN && s.chords.HeldCount() == 0 {
		s.chordSawAltShift = false
		if s.tracker != nil {
			s.chordStartLayout = s.tracker.State()
		}
	}
	s.noteChordAltShift(event)
	if action, fired := s.chords.Feed(event.Code, event.Value); fired {
		s.converter.Push(event.Code, event.Value) // keep shift bookkeeping consistent
		s.handleChord(action)
		return true
	}
	return false
}

// noteChordAltShift keeps the physical Alt state and records a press order in
// which the GNOME layout bindings (<Alt>Shift_L, <Shift>Alt_L) could fire.
func (s *Switcher) noteChordAltShift(event *InputEvent) {
	var bit uint8
	switch event.Code {
	case KEY_LEFTALT:
		bit = 1
	case KEY_RIGHTALT:
		bit = 2
	}
	switch event.Value {
	case K_DOWN:
		if Shifts[event.Code] && s.forkAltDown != 0 {
			s.chordSawAltShift = true
		}
		if event.Code == KEY_LEFTALT && (s.leftShiftPressed.Load() || s.rightShiftPressed.Load()) {
			s.chordSawAltShift = true
		}
		s.forkAltDown |= bit
	case K_UP:
		s.forkAltDown &^= bit
	}
}

// handleChord runs a fired chord action on the Run goroutine.
func (s *Switcher) handleChord(action ChordAction) {
	s.logDebug("chord: %s", action)
	if s.auto != nil {
		s.auto.Cancel(forkReasonChord)
	}
	switch action {
	case ChordToggleAuto:
		// Drop the chord's shift events (switcher.go trimBuffer precedent) so
		// that a double-Shift right after it still matches.
		s.converter.trimBuffer()
		_ = s.SetAutoEnabled(!s.autoEnabled.Load(), ipc.SourceChord)
	case ChordConvertSelection:
		s.ctrlShiftSelectArmed.Store(false)
		s.pendingBufferAction = ActionNone
		s.converter.ClearBuffer() // explicit (C12)
		s.restoreLayoutIfChordFlipped()
		// Runs via the deferred finishSelectionModifierEvent of this event.
		s.deferSelectionUntilModifiersReleased(selectionConvertLayout)
	}
}

// forkBeforeConversion is the first statement of performConversion, before
// the ReverseMode swap, so it sees the raw gesture. The raw convert-word
// gesture right after an automatic fix undoes that fix (true: performConversion
// returns). Every other manual conversion disarms the undo.
func (s *Switcher) forkBeforeConversion(action Action) bool {
	if s.auto == nil {
		return false
	}
	if action == ActionConvertWord && s.auto.TryUndo(s.converter) {
		return true
	}
	s.auto.Cancel(forkReasonManual)
	return false
}

// afterInputDiscarded runs after InputReader.Flush in performConversion,
// after convertSelection's drainEventChan and at the end of a hotkey
// suspension: keystrokes the application already received were dropped, so
// the chord detector and the engine's held set are resynchronised from the
// kernel and the word in progress is tainted "discarded" (C25).
func (s *Switcher) afterInputDiscarded() {
	var down map[uint16]bool
	err := errForkNoInputReader
	if s.inputReader != nil {
		down, err = forkKeysDown(s.inputReader)
	}
	if err == nil {
		s.chords.Resync(down)
	} else {
		s.logDebug("key state unavailable after discarded input: %v", err)
		s.chords.Reset()
		down = nil
	}
	if s.auto != nil {
		s.auto.Reset(down)
	}
	s.lsKeysDown = nil
	s.forkAltDown = 0
	if down[KEY_LEFTALT] {
		s.forkAltDown |= 1
	}
	if down[KEY_RIGHTALT] {
		s.forkAltDown |= 2
	}
}
