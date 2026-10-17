package runtime

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arumata/gswitch/internal/gswitch/focus"
	"github.com/arumata/gswitch/internal/gswitch/ipc"
	"github.com/arumata/gswitch/internal/gswitch/lang"
	"github.com/arumata/gswitch/internal/gswitch/layoutstate"
	"github.com/arumata/gswitch/internal/gswitch/userconfig"
)

// gswitch-auto WP11 tests of the integration glue. Shared WP11 helpers carry
// the forkTest prefix. Replayed key events go to a regular file; no input
// device is created and no real session bus or IBus is contacted.

// Key rows in scancode order 2..13, 16..27, 30..41, 43..53 (47 keys), as in
// punto-rs keymap.rs (ru = XKB ru(winkeys)).
const (
	forkTestRowEN      = "1234567890-=qwertyuiop[]asdfghjkl;'`\\zxcvbnm,./"
	forkTestRowENShift = "!@#$%^&*()_+QWERTYUIOP{}ASDFGHJKL:\"~|ZXCVBNM<>?"
	forkTestRowRU      = "1234567890-=йцукенгшщзхъфывапролджэё\\ячсмитьбю."
	forkTestRowRUShift = "!\"№;%:?*()_+ЙЦУКЕНГШЩЗХЪФЫВАПРОЛДЖЭЁ/ЯЧСМИТЬБЮ,"
)

var forkTestNamedKeys = map[string]uint16{
	"SP": KEY_SPACE, "EN": KEY_ENTER, "BS": KEY_BACKSPACE, "TAB": KEY_TAB,
	"L": KEY_LEFTSHIFT, "R": KEY_RIGHTSHIFT,
	"LCTRL": KEY_LEFTCTRL, "RCTRL": KEY_RIGHTCTRL, "LALT": KEY_LEFTALT, "RALT": KEY_RIGHTALT,
	"PAUSE": testKeyPause,
}

func forkTestRowCodes() []uint16 {
	codes := make([]uint16, 0, 47)
	for _, r := range [][2]uint16{{2, 13}, {16, 27}, {30, 41}, {43, 53}} {
		for c := r[0]; c <= r[1]; c++ {
			codes = append(codes, c)
		}
	}
	return codes
}

func forkTestRowLayout(name, normal, shifted string) *LayoutInfo {
	l := &LayoutInfo{Name: name, KeyMap: make(map[string][]rune), Unmapped: make(map[string]bool)}
	n, s := []rune(normal), []rune(shifted)
	for i, code := range forkTestRowCodes() {
		key, _ := XKBKeyName(code)
		l.KeyMap[key] = []rune{n[i], s[i]}
	}
	return l
}

// forkTestLayouts returns fixture us and ru layouts.
func forkTestLayouts() (us, ru *LayoutInfo) {
	return forkTestRowLayout("us", forkTestRowEN, forkTestRowENShift),
		forkTestRowLayout("ru", forkTestRowRU, forkTestRowRUShift)
}

// forkTestKeyCode maps a script token (a character of forkTestRowEN or a name
// of forkTestNamedKeys) to its scancode.
func forkTestKeyCode(tok string) (uint16, bool) {
	if code, ok := forkTestNamedKeys[tok]; ok {
		return code, true
	}
	runes := []rune(tok)
	if len(runes) != 1 {
		return 0, false
	}
	idx := slices.Index([]rune(forkTestRowEN), runes[0])
	if idx < 0 {
		return 0, false
	}
	return forkTestRowCodes()[idx], true
}

// forkTestEvents parses a key script: each token is a key, tapped (down and
// up) by default; a "+" suffix sends only the K_DOWN, "-" only the K_UP and
// "~" a K_REPEAT.
func forkTestEvents(t testing.TB, script string) []InputEvent {
	t.Helper()
	var events []InputEvent
	for _, tok := range strings.Fields(script) {
		values := []int32{K_DOWN, K_UP}
		if n := len(tok); n > 1 {
			switch tok[n-1] {
			case '+':
				values, tok = []int32{K_DOWN}, tok[:n-1]
			case '-':
				values, tok = []int32{K_UP}, tok[:n-1]
			case '~':
				values, tok = []int32{K_REPEAT}, tok[:n-1]
			}
		}
		code, ok := forkTestKeyCode(tok)
		if !ok {
			t.Fatalf("forkTestEvents: unknown token %q", tok)
		}
		for _, v := range values {
			events = append(events, InputEvent{Type: EV_KEY, Code: code, Value: v})
		}
	}
	return events
}

// forkTestFeed runs a key script through the real processKeyEvent.
func forkTestFeed(t testing.TB, s *Switcher, script string) {
	t.Helper()
	for _, ev := range forkTestEvents(t, script) {
		s.processKeyEvent(&ev)
	}
}

// forkTestKeyboard returns a VirtualKeyboard writing to a temporary file.
func forkTestKeyboard(t *testing.T) (*VirtualKeyboard, *os.File) {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "events")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { out.Close() })
	return &VirtualKeyboard{fd: out}, out
}

// forkTestCaptured decodes the EV_KEY records written to out so far.
func forkTestCaptured(t *testing.T, out *os.File) []KeyEvent {
	t.Helper()
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	events := []KeyEvent{}
	for {
		var ev InputEvent
		err := binary.Read(out, binary.LittleEndian, &ev)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Type == EV_KEY {
			events = append(events, KeyEvent{Code: ev.Code, Value: ev.Value})
		}
	}
	if _, err := out.Seek(0, io.SeekEnd); err != nil {
		t.Fatal(err)
	}
	return events
}

// forkTestCanonical sorts the leading and trailing runs of shift releases,
// which upstream emits in map iteration order.
func forkTestCanonical(events []KeyEvent) []KeyEvent {
	out := slices.Clone(events)
	isShiftUp := func(ev KeyEvent) bool { return Shifts[ev.Code] && ev.Value == K_UP }
	byCode := func(a, b KeyEvent) int { return int(a.Code) - int(b.Code) }
	n := 0
	for n < len(out) && n < len(Shifts) && isShiftUp(out[n]) {
		n++
	}
	slices.SortStableFunc(out[:n], byCode)
	i := len(out)
	for i > n && isShiftUp(out[i-1]) {
		i--
	}
	slices.SortStableFunc(out[i:], byCode)
	return out
}

func forkTestCountCode(events []KeyEvent, code uint16) int {
	n := 0
	for _, ev := range events {
		if ev.Code == code && ev.Value == K_DOWN {
			n++
		}
	}
	return n
}

// forkTestSwitcher builds a Switcher as the upstream tests do (struct
// literal) plus the fork's control queue; nothing else of the fork is set.
func forkTestSwitcher(t *testing.T, cfg *Config) (*Switcher, *os.File) {
	t.Helper()
	keyboard, out := forkTestKeyboard(t)
	s := &Switcher{
		config:      cfg,
		converter:   NewConverter(),
		vKeyboard:   keyboard,
		inputReader: &InputReader{},
	}
	s.converter.ConvKey = cfg.ConvertKey
	s.converter.LSKeys = cfg.LayoutSwitchKey
	s.controlChan = make(chan func(), forkControlQueue)
	return s, out
}

// forkTestWithLayouts installs the fixture us/ru pair.
func forkTestWithLayouts(s *Switcher) {
	us, ru := forkTestLayouts()
	s.layoutConverter = NewLayoutConverter(us, ru)
	s.layoutSpecs = []LayoutSpec{{Name: "us"}, {Name: "ru"}}
}

// forkTestDefaultChords installs the default chords (42+54, 42+97+100).
func forkTestDefaultChords(s *Switcher) {
	s.applyChords(nil, userconfig.DefaultSettings())
}

// forkTestDrainControl runs every queued control function, as the Run loop
// would.
func forkTestDrainControl(s *Switcher) {
	for {
		select {
		case fn := <-s.controlChan:
			fn()
		default:
			return
		}
	}
}

// forkTestConfig wraps settings and word/app entries in a loaded Config.
func forkTestConfig(st userconfig.Settings, words, apps []string) *userconfig.Config {
	return &userconfig.Config{
		Settings: st,
		Words:    userconfig.NewWordSet(words),
		Apps:     userconfig.NewAppSet(apps),
	}
}

func forkTestTracker(t *testing.T) *layoutstate.Tracker {
	t.Helper()
	tr := layoutstate.New(layoutstate.Options{
		Layouts: [2]layoutstate.Layout{{Name: "us"}, {Name: "ru"}},
	})
	t.Cleanup(tr.Stop)
	return tr
}

// forkTestClock replaces forkNow with a manual clock.
type forkTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func forkTestSetClock(t *testing.T) *forkTestClock {
	t.Helper()
	c := &forkTestClock{now: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)}
	orig := forkNow
	forkNow = c.Now
	t.Cleanup(func() { forkNow = orig })
	return c
}

func (c *forkTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *forkTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// forkTestIPC records what the switcher publishes over IPC.
type forkTestIPC struct {
	mu        sync.Mutex
	statuses  []ipc.Status
	toggles   []forkTestToggle
	corrected []ipc.Correction
	undone    []forkTestUndo
	closed    int
}

type forkTestToggle struct {
	enabled bool
	source  string
}

type forkTestUndo struct {
	word    string
	learned bool
}

func (f *forkTestIPC) UpdateStatus(st ipc.Status) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, st)
}

func (f *forkTestIPC) NotifyAutoEnabled(enabled bool, source string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.toggles = append(f.toggles, forkTestToggle{enabled, source})
}

func (f *forkTestIPC) NotifyCorrected(c ipc.Correction) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.corrected = append(f.corrected, c)
}

func (f *forkTestIPC) NotifyUndone(word string, learned bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.undone = append(f.undone, forkTestUndo{word, learned})
}

func (f *forkTestIPC) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
}

func (f *forkTestIPC) lastToggle() (forkTestToggle, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.toggles) == 0 {
		return forkTestToggle{}, 0
	}
	return f.toggles[len(f.toggles)-1], len(f.toggles)
}

func forkTestAttachIPC(s *Switcher) *forkTestIPC {
	tap := &forkTestIPC{}
	s.ipcSvc.Store(&forkIPCRef{svc: tap})
	return tap
}

// forkTestJudge is a fake detector with a fixed verdict.
type forkTestJudge struct {
	verdict lang.Verdict
	calls   int
}

func (j *forkTestJudge) Decide(string, string, lang.Pair) lang.Decision {
	j.calls++
	reason := lang.ReasonScores
	if j.verdict != lang.Fix {
		reason = lang.ReasonShownKnown
	}
	return lang.Decision{Verdict: j.verdict, Reason: reason}
}

// forkTestEngine is an auto engine with fake dependencies installed as
// s.auto, so that the real switcher hooks drive it.
type forkTestEngine struct {
	clock    *forkTestClock
	judge    *forkTestJudge
	layout   LayoutSnapshot
	focusSeq uint64
	emitErr  error
	emits    [][]KeyEvent
	undos    []forkTestUndo
	settings AutoSettings
}

func forkTestFakeEngine(t *testing.T, s *Switcher) *forkTestEngine {
	t.Helper()
	env := &forkTestEngine{
		clock:  forkTestSetClock(t),
		judge:  &forkTestJudge{verdict: lang.Fix},
		layout: LayoutSnapshot{Index: 0, Generation: 1, Confirmed: true},
		settings: AutoSettings{
			Enabled:        true,
			MaxLetters:     24,
			Holdoff:        120 * time.Millisecond,
			LearnOnUndo:    true,
			LearnUndoCount: 1,
		},
	}
	us, ru := forkTestLayouts()
	eng, err := NewAutoEngine(AutoDeps{
		Layouts: [2]*LayoutInfo{us, ru},
		Langs:   [2]lang.Lang{lang.En, lang.Ru},
		Judge:   env.judge,
		Layout:  func() LayoutSnapshot { return env.layout },
		Focus:   func() FocusSnapshot { return FocusSnapshot{Seq: env.focusSeq, Allowed: true} },
		Emit: func(events []KeyEvent) error {
			if env.emitErr != nil {
				return env.emitErr
			}
			env.emits = append(env.emits, slices.Clone(events))
			return nil
		},
		Settings: func() AutoSettings { return env.settings },
		OnUndo: func(word string, learn bool) {
			env.undos = append(env.undos, forkTestUndo{word, learn})
		},
		Now: env.clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.auto = eng
	return env
}

// forkTestFire types "ghbdtn " through the hooks and lets the armed fix fire.
func forkTestFire(t *testing.T, s *Switcher, env *forkTestEngine) {
	t.Helper()
	before := len(env.emits)
	forkTestFeed(t, s, "g h b d t n SP")
	env.clock.Advance(env.settings.Holdoff + time.Millisecond)
	s.autoTick()
	if len(env.emits) != before+1 || s.auto.lastFix == nil {
		t.Fatalf("fix did not fire: emits %d -> %d, stats %+v", before, len(env.emits), s.auto.Stats())
	}
}

func TestDefaultMinLettersAgree(t *testing.T) {
	if got, want := userconfig.DefaultSettings().MinLetters, lang.DefaultOptions().MinLetters; got != want {
		t.Fatalf("userconfig default auto-min-letters = %d, lang default MinLetters = %d", got, want)
	}
}

func TestApplyUserConfigDisablesChordContainingLSKeys(t *testing.T) {
	var selections []selectionTransform
	s, _ := forkTestSwitcher(t, &Config{LayoutSwitchKey: []uint16{KEY_LEFTALT, KEY_LEFTSHIFT}})
	s.selectionHandler = func(tr selectionTransform) { selections = append(selections, tr) }
	tap := forkTestAttachIPC(s)

	st := userconfig.DefaultSettings()
	st.ToggleChord = userconfig.Chord{KEY_LEFTSHIFT, KEY_LEFTALT, KEY_RIGHTCTRL} // contains LSKeys 56+42
	s.applyUserConfig(forkTestConfig(st, nil, nil))

	forkTestFeed(t, s, "L+ LALT+ RCTRL+ RCTRL- LALT- L-")
	if s.AutoEnabled() {
		t.Fatal("chord containing the layout switch keys toggled auto mode")
	}
	if _, n := tap.lastToggle(); n != 0 {
		t.Fatalf("toggle notifications = %d, want 0", n)
	}
	status := s.statusSnapshot()
	if status.ToggleChord != "none" || status.SelectionChord != "42+97+100" {
		t.Fatalf("effective chords = %q / %q, want none / 42+97+100", status.ToggleChord, status.SelectionChord)
	}
	if !slices.ContainsFunc(status.ConfigWarnings, func(w string) bool {
		return strings.Contains(w, userconfig.KeyToggleChord) && strings.Contains(w, "layout switch keys")
	}) {
		t.Fatalf("ConfigWarnings = %q, want the disabled toggle chord", status.ConfigWarnings)
	}

	// The selection chord stays active.
	forkTestFeed(t, s, "L+ RALT+ RCTRL+ RCTRL- RALT- L-")
	if !slices.Equal(selections, []selectionTransform{selectionConvertLayout}) {
		t.Fatalf("selection calls = %v, want one layout conversion", selections)
	}

	// A chord containing the convert key is disabled as well.
	s.config.ConvertKey, s.converter.ConvKey = testKeyPause, testKeyPause
	st.ToggleChord = userconfig.Chord{KEY_LEFTSHIFT, testKeyPause}
	s.applyUserConfig(forkTestConfig(st, nil, nil))
	if got := s.statusSnapshot().ToggleChord; got != "none" {
		t.Fatalf("toggle chord with the convert key = %q, want none", got)
	}

	// A valid chord applies immediately, without restart.
	s.applyUserConfig(forkTestConfig(userconfig.DefaultSettings(), nil, nil))
	forkTestFeed(t, s, "L+ R+ R- L-")
	if !s.AutoEnabled() {
		t.Fatal("default toggle chord did not toggle after reload")
	}
}

func TestAutoEnabledPersistenceRule(t *testing.T) {
	newSwitcher := func(t *testing.T) (*Switcher, *forkTestIPC, *forkTestClock) {
		clock := forkTestSetClock(t)
		s, _ := forkTestSwitcher(t, &Config{})
		tap := forkTestAttachIPC(s)
		s.applyUserConfig(forkTestConfig(userconfig.DefaultSettings(), nil, nil)) // file: false
		return s, tap, clock
	}
	fileWith := func(enabled bool) *userconfig.Config {
		st := userconfig.DefaultSettings()
		st.AutoEnabled = enabled
		return forkTestConfig(st, nil, nil)
	}

	t.Run("chord toggle until the file catches up", func(t *testing.T) {
		s, tap, clock := newSwitcher(t)
		forkTestFeed(t, s, "L+ R+ R- L-")
		if !s.AutoEnabled() || !s.statusSnapshot().AutoEnabledUnsaved {
			t.Fatalf("after chord: enabled=%v unsaved=%v, want true/true", s.AutoEnabled(), s.autoUnsaved.Load())
		}
		if last, n := tap.lastToggle(); n != 1 || last != (forkTestToggle{true, ipc.SourceChord}) {
			t.Fatalf("toggle notifications = %d, last %+v", n, last)
		}
		clock.Advance(300 * time.Millisecond)
		s.applyUserConfig(fileWith(true)) // the tray persisted it
		if !s.AutoEnabled() || s.statusSnapshot().AutoEnabledUnsaved {
			t.Fatalf("after the file caught up: enabled=%v unsaved=%v, want true/false",
				s.AutoEnabled(), s.autoUnsaved.Load())
		}
		if _, n := tap.lastToggle(); n != 1 {
			t.Fatalf("the file catching up announced a toggle (%d notifications)", n)
		}
	})

	t.Run("late echo of an earlier toggle is ignored", func(t *testing.T) {
		s, tap, clock := newSwitcher(t)
		forkTestFeed(t, s, "L+ R+ R- L-") // on
		clock.Advance(400 * time.Millisecond)
		forkTestFeed(t, s, "L+ R+ R- L-") // off within a second
		clock.Advance(400 * time.Millisecond)
		s.applyUserConfig(fileWith(true)) // the tray's write of "true" lands late
		if s.AutoEnabled() {
			t.Fatal("stale file value switched auto mode back on")
		}
		if !s.statusSnapshot().AutoEnabledUnsaved {
			t.Fatal("AutoEnabledUnsaved = false after an ignored echo, want true (the tray writes false)")
		}
		if last, n := tap.lastToggle(); n != 2 || last != (forkTestToggle{false, ipc.SourceChord}) {
			t.Fatalf("toggle notifications = %d, last %+v; want 2, off by chord", n, last)
		}
		s.applyUserConfig(fileWith(false))
		if s.AutoEnabled() || s.statusSnapshot().AutoEnabledUnsaved {
			t.Fatal("file matching the runtime value did not clear AutoEnabledUnsaved")
		}
	})

	t.Run("hand edit is applied with source config", func(t *testing.T) {
		s, tap, clock := newSwitcher(t)
		clock.Advance(time.Minute)
		s.applyUserConfig(fileWith(true))
		if !s.AutoEnabled() || s.statusSnapshot().AutoEnabledUnsaved {
			t.Fatalf("hand edit: enabled=%v unsaved=%v, want true/false", s.AutoEnabled(), s.autoUnsaved.Load())
		}
		if last, n := tap.lastToggle(); n != 1 || last != (forkTestToggle{true, ipc.SourceConfig}) {
			t.Fatalf("toggle notifications = %d, last %+v; want on by config", n, last)
		}
	})

	t.Run("dbus toggle marks unsaved", func(t *testing.T) {
		s, tap, _ := newSwitcher(t)
		if err := s.SetAutoEnabled(true, ipc.SourceDBus); err != nil {
			t.Fatal(err)
		}
		if !s.statusSnapshot().AutoEnabledUnsaved {
			t.Fatal("D-Bus toggle did not mark the state unsaved")
		}
		if last, _ := tap.lastToggle(); last != (forkTestToggle{true, ipc.SourceDBus}) {
			t.Fatalf("last toggle = %+v, want on by dbus", last)
		}
		// The same value again is no change and announces nothing.
		_ = s.SetAutoEnabled(true, ipc.SourceDBus)
		if _, n := tap.lastToggle(); n != 1 {
			t.Fatalf("unchanged D-Bus value announced a toggle (%d notifications)", n)
		}
	})
}

func TestLearnedWordsPendingUntilFileHasThem(t *testing.T) {
	s, _ := forkTestSwitcher(t, &Config{})
	tap := forkTestAttachIPC(s)
	c := forkTestConfig(userconfig.DefaultSettings(), nil, nil)
	s.userCfg.Store(c)
	s.initDetector(c)

	s.autoOnUndo("ghbdtn", true)
	if got := s.statusSnapshot().PendingLearnedWords; !slices.Equal(got, []string{"ghbdtn"}) {
		t.Fatalf("PendingLearnedWords = %q, want [ghbdtn]", got)
	}
	if !s.detector.IsException("ghbdtn", lang.En) {
		t.Fatal("learned word is not a detector exception")
	}
	if len(tap.undone) != 1 || tap.undone[0] != (forkTestUndo{"ghbdtn", true}) {
		t.Fatalf("undo notifications = %+v", tap.undone)
	}
	s.autoOnUndo("rfr", false) // undone but not learned
	if got := s.statusSnapshot().PendingLearnedWords; !slices.Equal(got, []string{"ghbdtn"}) {
		t.Fatalf("PendingLearnedWords after an undo without learning = %q", got)
	}

	hidden := userconfig.DefaultSettings()
	hidden.ExposeWords = false
	s.applyUserConfig(forkTestConfig(hidden, nil, nil))
	if got := s.statusSnapshot().PendingLearnedWords; len(got) != 0 {
		t.Fatalf("PendingLearnedWords with ipc-expose-words=false = %q, want empty", got)
	}
	if !s.detector.IsException("ghbdtn", lang.En) {
		t.Fatal("pending word lost from the detector on reload")
	}
	s.autoOnUndo("ytn", true)
	if last := tap.undone[len(tap.undone)-1]; last != (forkTestUndo{"", true}) {
		t.Fatalf("undo notification with ipc-expose-words=false = %+v, want blank word", last)
	}

	// Back to ipc-expose-words=true: "ytn", learned while words were hidden,
	// stays in daemon memory only (never published, so never saved).
	s.applyUserConfig(forkTestConfig(userconfig.DefaultSettings(), []string{"Ghbdtn"}, nil))
	if got := s.statusSnapshot().PendingLearnedWords; len(got) != 0 {
		t.Fatalf("PendingLearnedWords after the file got ghbdtn = %q, want empty", got)
	}
	for _, w := range []string{"ghbdtn", "ytn"} {
		if !s.detector.IsException(w, lang.En) {
			t.Fatalf("%q is not a detector exception after reload", w)
		}
	}
	s.applyUserConfig(forkTestConfig(userconfig.DefaultSettings(), []string{"Ghbdtn"}, nil))
	if !s.detector.IsException("ytn", lang.En) {
		t.Fatal("memory-only learned word lost from the detector on a second reload")
	}
	s.learnWord("ghbdtn") // already in the file
	s.learnWord("ntrcn")
	if got := s.statusSnapshot().PendingLearnedWords; !slices.Equal(got, []string{"ntrcn"}) {
		t.Fatalf("PendingLearnedWords = %q, want [ntrcn] (file word and memory-only word excluded)", got)
	}
}

// TestWatchUserConfigAppliesChangeBeforeWatchStart: a change made between
// initFork's Load and the start of the watcher (the tray seeding
// exceptions-apps.txt at the first login) is applied, not taken as the
// watcher's baseline.
func TestWatchUserConfigAppliesChangeBeforeWatchStart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), userconfig.DirName)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	s, _ := forkTestSwitcher(t, &Config{})
	s.userDir = dir
	c, _ := userconfig.Load(dir)
	s.userCfg.Store(c)
	if err := userconfig.EnsureFiles(dir); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.forkWG.Add(1)
	go s.watchUserConfig(ctx)
	defer func() {
		cancel()
		s.forkWG.Wait()
	}()
	deadline := time.Now().Add(3 * time.Second)
	for !s.userCfg.Load().Apps.MatchApp("org.keepassxc.keepassxc", "") {
		if time.Now().After(deadline) {
			t.Fatalf("seeded exclusion list not applied: apps %q", s.userCfg.Load().Apps.Entries())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestLearnWordConcurrentReloadKeepsWord: a reload that runs at the same time
// as learnWord (watcher or D-Bus Reload goroutine against the Run goroutine)
// never drops the newly learned word from the detector.
func TestLearnWordConcurrentReloadKeepsWord(t *testing.T) {
	s, _ := forkTestSwitcher(t, &Config{})
	c := forkTestConfig(userconfig.DefaultSettings(), nil, nil)
	s.userCfg.Store(c)
	s.initDetector(c)
	const letters = "abcdefghijklmnopqrstuvwxyz"
	for round := range 3000 {
		word := "q" + string(letters[round%26]) + string(letters[round/26%26]) + string(letters[round/676%26])
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			s.applyUserConfig(forkTestConfig(userconfig.DefaultSettings(), nil, nil))
		}()
		go func() {
			defer wg.Done()
			<-start
			s.learnWord(word)
		}()
		close(start)
		wg.Wait()
		if !s.detector.IsException(word, lang.En) {
			t.Fatalf("round %d: %q learned during a reload is not a detector exception", round, word)
		}
	}
}

// forkTestDirSnapshot records name, mode, size and modification time of
// every entry of dir.
func forkTestDirSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	snap := make(map[string]string)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		snap[path] = fmt.Sprintf("%s %d %d", info.Mode(), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestDaemonNeverWrites(t *testing.T) {
	t.Run("runtime state changes leave a read-only directory untouched", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root can write to read-only directories")
		}
		base := t.TempDir()
		dir := filepath.Join(base, userconfig.DirName)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		files := map[string]string{
			userconfig.AutoConfName: "auto-enabled=false\nlayout-source=none\nfocus-source=off\n",
			userconfig.WordsName:    "# words\n",
			userconfig.AppsName:     "# apps\n",
		}
		for name, text := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o400); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		t.Setenv("XDG_CONFIG_HOME", base)

		s, _ := forkTestSwitcher(t, &Config{})
		forkTestWithLayouts(s)
		s.initFork()
		if s.userDir != dir {
			t.Fatalf("userDir = %q, want %q", s.userDir, dir)
		}
		before := forkTestDirSnapshot(t, dir)

		forkTestFeed(t, s, "L+ R+ R- L-") // chord toggle
		if !s.AutoEnabled() || !s.autoUnsaved.Load() {
			t.Fatal("chord toggle did not change the runtime state")
		}
		s.autoOnUndo("ghbdtn", true) // learn
		if err := s.ReloadUserConfig(); err != nil {
			t.Fatalf("ReloadUserConfig() = %v", err)
		}
		s.SuspendHotkeys(true)
		s.SuspendHotkeys(false)
		forkTestDrainControl(s)

		if after := forkTestDirSnapshot(t, dir); !maps.Equal(before, after) {
			t.Fatalf("configuration directory changed:\nbefore %v\nafter  %v", before, after)
		}
		if w := s.statusSnapshot().ConfigWarnings; len(w) != 0 {
			t.Fatalf("ConfigWarnings = %q, want none", w)
		}
		if got := s.statusSnapshot().PendingLearnedWords; !slices.Equal(got, []string{"ghbdtn"}) {
			t.Fatalf("PendingLearnedWords = %q, want the learned word kept for the tray", got)
		}
	})

	t.Run("runtime never calls a userconfig writer", func(t *testing.T) {
		writer := regexp.MustCompile(`\buserconfig\.(Save|Update|SetAutoEnabled|AppendListEntry|` +
			`RemoveListEntry|ApplyListDiff|WriteListText|EnsureFiles)\b`)
		entries, err := os.ReadDir(".")
		if err != nil {
			t.Fatal(err)
		}
		scanned := 0
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			data, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			scanned++
			if loc := writer.FindIndex(data); loc != nil {
				t.Errorf("%s calls %s: the daemon must never write the user configuration", name, data[loc[0]:loc[1]])
			}
		}
		if scanned == 0 {
			t.Fatal("no source files scanned")
		}
	})
}

func TestExcludedAppMatchesRawExtensionID(t *testing.T) {
	apps := userconfig.NewAppSet([]string{"org.gnome.ptyxis"})
	raw := focus.State{
		Available: true,
		AppID:     "org.gnome.Ptyxis.desktop",
		WmClass:   "org.gnome.Ptyxis",
		Purpose:   focus.PurposeNormal,
		Seq:       4,
	}

	// After the client's normalisation (AppKey/WmClassKey filled).
	ok, reason := forkFocusDecision(raw, &focus.Policy{Excluded: apps})
	if ok || reason != focus.ReasonExcludedApp {
		t.Fatalf("raw Ptyxis id: Allow = %v, %q; want denied excluded-app", ok, reason)
	}
	// MatchApp called directly with the raw id.
	if !apps.MatchApp(raw.AppID, "") {
		t.Fatal("ListSet.MatchApp(raw id) = false")
	}
	// A nil list stored in the interface allows without panicking.
	if ok, reason := (focus.Policy{Excluded: (*userconfig.ListSet)(nil)}).Allow(forkNormalizeFocus(raw)); !ok {
		t.Fatalf("nil ListSet: Allow = false, %q", reason)
	}

	// A raw id entered by hand into exceptions-apps.txt matches too.
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, userconfig.AppsName), []byte("org.gnome.Ptyxis.desktop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := userconfig.Load(base)
	if err != nil {
		t.Fatal(err)
	}
	if ok, reason := forkFocusDecision(raw, forkFocusPolicy(c)); ok || reason != focus.ReasonExcludedApp {
		t.Fatalf("hand-entered raw id: Allow = %v, %q; want denied excluded-app", ok, reason)
	}
	other := raw
	other.AppID, other.WmClass = "org.gnome.TextEditor.desktop", "gnome-text-editor"
	if ok, _ := forkFocusDecision(other, forkFocusPolicy(c)); !ok {
		t.Fatal("an application not in the list was denied")
	}

	// The engine's view through the switcher: no focus client means the
	// extension is unavailable, which is allowed unless auto-require-focus.
	s, _ := forkTestSwitcher(t, &Config{})
	s.focusPolicy.Store(forkFocusPolicy(c))
	if snap := s.autoFocusSnapshot(); !snap.Allowed {
		t.Fatalf("no focus client: snapshot %+v, want allowed (fail-open)", snap)
	}
	st := userconfig.DefaultSettings()
	st.RequireFocus = true
	s.focusPolicy.Store(forkFocusPolicy(forkTestConfig(st, nil, nil)))
	if snap := s.autoFocusSnapshot(); snap.Allowed || snap.Reason != focus.ReasonFocusUnavailable {
		t.Fatalf("auto-require-focus without focus: snapshot %+v, want focus-unavailable", snap)
	}
}

type forkTestResync struct{ layout layoutstate.Layout }

func (r forkTestResync) Current(context.Context) (layoutstate.Layout, error) { return r.layout, nil }

func TestStatusAvailabilityMapping(t *testing.T) {
	s, _ := forkTestSwitcher(t, &Config{})
	forkTestWithLayouts(s)
	tr := forkTestTracker(t)
	s.tracker = tr
	forkTestFakeEngine(t, s)

	check := func(label string, wantAvailable bool, wantReason string) ipc.Status {
		t.Helper()
		st := s.statusSnapshot()
		if st.AutoAvailable != wantAvailable || st.UnavailableReason != wantReason {
			t.Fatalf("%s: AutoAvailable=%v reason=%q, want %v %q",
				label, st.AutoAvailable, st.UnavailableReason, wantAvailable, wantReason)
		}
		return st
	}

	check("unknown", false, autoUnavailableLayoutUnknown)
	tr.Engine(layoutstate.SourceIBus, "xkb:ru::rus")
	st := check("confirmed", true, "")
	if st.ActiveLayout != "ru" || !st.LayoutKnown || st.LayoutSource != layoutstate.SourceIBus {
		t.Fatalf("confirmed: layout %q known=%v source=%q", st.ActiveLayout, st.LayoutKnown, st.LayoutSource)
	}
	tr.NoteSwitchHint()
	if st := check("pending", true, ""); st.LayoutKnown {
		t.Fatal("pending: LayoutKnown = true")
	}

	settingsOnly := forkTestTracker(t)
	settingsOnly.AddResync(forkTestResync{layout: layoutstate.Layout{Name: "us"}})
	if err := settingsOnly.Resync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := settingsOnly.State().Confidence; got != layoutstate.Settings {
		t.Fatalf("resync-only tracker confidence = %s, want settings", got)
	}
	s.tracker = settingsOnly
	check("settings", false, autoUnavailableLayoutUnknown)

	s.auto = nil
	s.autoReason = autoUnavailableLayoutSourceOf
	check("no engine", false, autoUnavailableLayoutSourceOf)
}

func TestEnableAutoReasons(t *testing.T) {
	ready := func(t *testing.T) *Switcher {
		t.Helper()
		s, _ := forkTestSwitcher(t, &Config{})
		forkTestWithLayouts(s)
		s.tracker = forkTestTracker(t)
		c := forkTestConfig(userconfig.DefaultSettings(), nil, nil)
		s.userCfg.Store(c)
		s.initDetector(c)
		return s
	}
	tests := []struct {
		name   string
		prep   func(t *testing.T, s *Switcher)
		reason string
	}{
		{name: "available", prep: func(*testing.T, *Switcher) {}, reason: ""},
		{name: "degraded", prep: func(_ *testing.T, s *Switcher) { s.degradedMode = true }, reason: autoUnavailableDegraded},
		{name: "no layout converter", prep: func(_ *testing.T, s *Switcher) { s.layoutConverter = nil }, reason: autoUnavailableNoConverter},
		{name: "unsupported pair", prep: func(_ *testing.T, s *Switcher) {
			us, _ := forkTestLayouts()
			s.layoutConverter = NewLayoutConverter(us, &LayoutInfo{Name: "de", KeyMap: map[string][]rune{}})
		}, reason: autoUnavailableUnsupported},
		{name: "same language twice", prep: func(_ *testing.T, s *Switcher) {
			us, _ := forkTestLayouts()
			us2, _ := forkTestLayouts()
			s.layoutConverter = NewLayoutConverter(us, us2)
		}, reason: autoUnavailableUnsupported},
		{name: "no tracker", prep: func(_ *testing.T, s *Switcher) { s.tracker = nil }, reason: autoUnavailableLayoutSourceOf},
		{name: "root mode", prep: func(t *testing.T, s *Switcher) {
			orig := forkGeteuid
			forkGeteuid = func() int { return 0 }
			t.Cleanup(func() { forkGeteuid = orig })
			s.tracker = nil
		}, reason: autoUnavailableRootMode},
		{name: "no detector", prep: func(_ *testing.T, s *Switcher) { s.detector = nil }, reason: autoUnavailableDetectorError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := ready(t)
			tt.prep(t, s)
			s.EnableAuto()
			if s.autoReason != tt.reason {
				t.Fatalf("autoReason = %q, want %q", s.autoReason, tt.reason)
			}
			if (s.auto != nil) != (tt.reason == "") {
				t.Fatalf("auto engine built = %v, want %v", s.auto != nil, tt.reason == "")
			}
			st := s.statusSnapshot()
			if tt.reason != "" && (st.AutoAvailable || st.UnavailableReason != tt.reason) {
				t.Fatalf("status AutoAvailable=%v reason=%q, want false %q", st.AutoAvailable, st.UnavailableReason, tt.reason)
			}
		})
	}
}

func TestStatusSnapshotBlanksWordsWhenNotExposed(t *testing.T) {
	s, _ := forkTestSwitcher(t, &Config{})
	forkTestWithLayouts(s)
	tap := forkTestAttachIPC(s)
	s.userCfg.Store(forkTestConfig(userconfig.DefaultSettings(), nil, nil))

	at := time.Unix(1_800_000_000, 0)
	s.autoOnFix(FixEvent{Shown: "ghbdtn", Alt: "привет", From: 0, To: 1, At: at})
	want := ipc.Correction{From: "ghbdtn", To: "привет", Direction: "us>ru", Time: at}
	if len(tap.corrected) != 1 || tap.corrected[0] != want {
		t.Fatalf("Corrected = %+v, want %+v", tap.corrected, want)
	}
	if got := s.statusSnapshot().Last; got != want {
		t.Fatalf("LastCorrection = %+v, want %+v", got, want)
	}

	hidden := userconfig.DefaultSettings()
	hidden.ExposeWords = false
	s.userCfg.Store(forkTestConfig(hidden, nil, nil))
	s.autoOnFix(FixEvent{Shown: "руддщ", Alt: "hello", From: 1, To: 0, At: at})
	blank := ipc.Correction{Direction: "ru>us", Time: at}
	if got := tap.corrected[1]; got != blank {
		t.Fatalf("Corrected with ipc-expose-words=false = %+v, want %+v", got, blank)
	}
	if got := s.statusSnapshot().Last; got != blank {
		t.Fatalf("LastCorrection with ipc-expose-words=false = %+v, want %+v", got, blank)
	}

	// Words captured while hidden stay hidden after ipc-expose-words=true.
	s.userCfg.Store(forkTestConfig(userconfig.DefaultSettings(), nil, nil))
	if got := s.statusSnapshot().Last; got != blank {
		t.Fatalf("LastCorrection after re-enabling ipc-expose-words = %+v, want %+v", got, blank)
	}
}

func TestStartStopForkWithoutSessionBus(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, userconfig.DirName)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, userconfig.AutoConfName),
		[]byte("layout-source=none\nfocus-source=off\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", base)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(base, "no-bus"))

	s, _ := forkTestSwitcher(t, &Config{})
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.initFork()
	s.startFork()
	if s.ipcSvc.Load() != nil {
		t.Fatal("IPC started without a session bus")
	}
	done := make(chan struct{})
	go func() {
		s.stopFork()
		s.stopFork()
		s.inputReader = nil // a zero InputReader would close fd 0
		s.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stopFork did not return")
	}
}

func TestRestartOnlyKeysWarn(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, userconfig.DirName)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, userconfig.AutoConfName),
		[]byte("layout-source=none\nfocus-source=off\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", base)
	s, _ := forkTestSwitcher(t, &Config{})
	s.initFork()
	if w := s.statusSnapshot().ConfigWarnings; len(w) != 0 {
		t.Fatalf("ConfigWarnings at start = %q, want none", w)
	}

	st := s.userSettings()
	st.LayoutSource = userconfig.SourceAuto
	st.LayoutHintTimeoutMs = 800
	st.MinLetters = 4 // hot-reloadable: no warning
	s.applyUserConfig(forkTestConfig(st, nil, nil))
	w := s.statusSnapshot().ConfigWarnings
	if len(w) != 2 || !strings.HasPrefix(w[0], "layout-source=auto takes effect after a restart") ||
		!strings.HasPrefix(w[1], "layout-hint-timeout-ms=800 takes effect after a restart") {
		t.Fatalf("ConfigWarnings = %q, want the two restart-only keys", w)
	}
	if s.tracker != nil {
		t.Fatal("layout-source change applied without a restart")
	}
}
