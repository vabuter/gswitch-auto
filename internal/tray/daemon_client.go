package tray

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/arumata/gswitch/internal/gswitch/ipc"
	"github.com/arumata/gswitch/internal/gswitch/userconfig"
)

// The tray side of the gswitch-auto daemon interface (D3) and of the user
// configuration directory (D2). Every D-Bus connection opened here is a
// PRIVATE session connection: fyne systray owns the shared one
// (systray_unix.go:192) and its stayRegistered loop breaks on foreign
// signals (C27).

const (
	daemonCallTimeout = 3 * time.Second
	// persistSkipWindow suppresses rewriting the same value while the daemon
	// has not yet seen the previous write.
	persistSkipWindow = 2 * time.Second
	// persistRetryDelay is the retry interval while the daemon still reports
	// unsaved state (or a write failed).
	persistRetryDelay = 5 * time.Second
)

// autoDaemonIPC is the part of *ipc.Client the tray uses.
type autoDaemonIPC interface {
	Start() error
	Stop()
	Status() ipc.ClientStatus
	SetAutoEnabled(ctx context.Context, enabled bool) error
	Reload(ctx context.Context) error
	SuspendHotkeys(ctx context.Context, suspend bool) error
}

var (
	// newAutoDaemonIPC builds the daemon client; tests replace it.
	newAutoDaemonIPC = func(cb ipc.ClientCallbacks) autoDaemonIPC { return ipc.NewClient(cb) }
	// connectForkSessionBus opens App.forkConn; tests replace it.
	connectForkSessionBus = func() (*dbus.Conn, error) { return dbus.ConnectSessionBus() }
)

// daemonClient wraps the IPC client with the persister and the hotkey
// suspension worker.
type daemonClient struct {
	client    autoDaemonIPC
	persister *daemonPersister
	suspender *hotkeySuspender
}

func newDaemonClient(a *App, dir string) *daemonClient {
	dc := &daemonClient{}
	dc.persister = newDaemonPersister(dir, a.onPersistResult)
	dc.client = newAutoDaemonIPC(ipc.ClientCallbacks{
		OnStatus: func(st ipc.ClientStatus) {
			dc.persister.offer(st)
			a.onDaemonStatus(st)
		},
	})
	dc.suspender = newHotkeySuspender(dc.client.SuspendHotkeys)
	return dc
}

func (dc *daemonClient) start() error {
	dc.persister.start()
	dc.suspender.start()
	return dc.client.Start()
}

func (dc *daemonClient) stop() {
	dc.client.Stop()
	dc.suspender.stop()
	dc.persister.stop()
}

// startFork wires the gswitch-auto parts of the tray: the user configuration
// directory (created 0700, seeded), its watcher, the private session
// connection for the focus extension helpers and the daemon client. It runs
// from onTrayReady after the upstream config watcher.
func (a *App) startFork() {
	dir, err := userconfig.Dir()
	if err != nil {
		fmt.Printf("Warning: user configuration directory: %v\n", err)
		dir = ""
	}
	if dir != "" {
		// EnsureFiles creates the directory 0700 (forcing 0700 on a directory
		// that upstream created 0750, C23) and seeds the missing files.
		if err := userconfig.EnsureFiles(dir); err != nil {
			fmt.Printf("Warning: failed to prepare %s: %v\n", dir, err)
		}
	}
	var cfg *userconfig.Config
	if dir != "" {
		cfg, _ = userconfig.Load(dir)
	}

	ctx, cancel := context.WithCancel(context.Background())
	conn, connErr := connectForkSessionBus()
	if connErr != nil {
		fmt.Printf("Warning: failed to open a session bus connection: %v\n", connErr)
		conn = nil
	}

	a.mu.Lock()
	if a.quitting {
		a.mu.Unlock()
		cancel()
		if conn != nil {
			_ = conn.Close()
		}
		return
	}
	a.userDir = dir
	a.userConfig = cfg
	a.forkCancel = cancel
	a.forkConn = conn
	a.mu.Unlock()

	if conn != nil {
		WatchFocusExtension(ctx, conn, a.onFocusExtensionChanged)
		go func() {
			info, err := QueryFocusExtension(ctx, conn)
			if err != nil && ctx.Err() != nil {
				return
			}
			a.onFocusExtensionChanged(info)
		}()
	}
	if dir != "" {
		go func() {
			// cfg is the baseline: a change made since the Load above is
			// reported once the watch is installed.
			if err := userconfig.Watch(ctx, dir, a.onUserConfigChanged, userconfig.Options{Baseline: cfg}); err != nil {
				fmt.Printf("Warning: failed to watch %s: %v\n", dir, err)
			}
		}()
	}

	dc := newDaemonClient(a, dir)
	if err := dc.start(); err != nil {
		fmt.Printf("Warning: failed to start the daemon client: %v\n", err)
	}
	a.mu.Lock()
	if a.quitting {
		a.mu.Unlock()
		dc.stop()
		return
	}
	a.daemon = dc
	a.mu.Unlock()
	setKeyPickerSuspend(dc.suspender.request)
	a.publishAutoState()
}

// stopFork undoes startFork; it runs from Quit.
func (a *App) stopFork() {
	a.mu.Lock()
	dc := a.daemon
	cancel := a.forkCancel
	conn := a.forkConn
	a.daemon = nil
	a.forkCancel = nil
	a.forkConn = nil
	a.mu.Unlock()

	setKeyPickerSuspend(nil)
	if cancel != nil {
		cancel()
	}
	if dc != nil {
		dc.stop()
	}
	if conn != nil {
		_ = conn.Close() // our private connection only, never the shared bus
	}
}

// ForkConn returns the tray's private session connection (nil when none).
func (a *App) ForkConn() *dbus.Conn {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.forkConn
}

// UserConfigDir returns the user configuration directory ("" when unknown).
func (a *App) UserConfigDir() string {
	if a == nil {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.userDir
}

// daemonStatus returns the last daemon status and whether a client exists.
func (a *App) daemonStatus() ipc.ClientStatus {
	if a == nil {
		return ipc.ClientStatus{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.autoStatus
}

// ReloadDaemon asks a connected daemon to reload the user configuration.
// It returns ipc.ErrNotConnected when no daemon is connected.
func (a *App) ReloadDaemon() error {
	if a == nil {
		return ipc.ErrNotConnected
	}
	a.mu.Lock()
	dc := a.daemon
	a.mu.Unlock()
	if dc == nil {
		return ipc.ErrNotConnected
	}
	ctx, cancel := context.WithTimeout(context.Background(), daemonCallTimeout)
	defer cancel()
	return dc.client.Reload(ctx)
}

// onDaemonStatus runs on the IPC client goroutine and must not block.
func (a *App) onDaemonStatus(st ipc.ClientStatus) {
	a.mu.Lock()
	a.autoStatus = st
	a.mu.Unlock()
	a.publishAutoState()
}

// onUserConfigChanged runs on the watcher goroutine (hand edits and the
// tray's own writes).
func (a *App) onUserConfigChanged(cfg *userconfig.Config) {
	a.mu.Lock()
	a.userConfig = cfg
	a.mu.Unlock()
	a.publishAutoState()
	scheduleGTK(func() {
		if w := a.currentSettingsWindow(); w != nil {
			w.onUserConfigChanged(cfg)
		}
	})
}

// onFocusExtensionChanged runs on a helper goroutine.
func (a *App) onFocusExtensionChanged(info FocusExtensionInfo) {
	a.mu.Lock()
	a.focusExtInfo = info
	a.focusExtKnown = true
	a.mu.Unlock()
	scheduleGTK(func() {
		if w := a.currentSettingsWindow(); w != nil {
			w.setFocusExtensionInfo(info)
		}
	})
}

// onPersistResult runs on the persister goroutine: message is the
// strGAErrorPersist text of a failed write, or "" after a success.
func (a *App) onPersistResult(message string) {
	a.mu.Lock()
	a.persistError = message
	tray := a.tray
	a.mu.Unlock()
	if message != "" {
		fmt.Printf("Warning: %s\n", message)
	}
	if tray != nil {
		tray.UpdateAutoPersistError(message)
	}
	scheduleGTK(func() {
		if w := a.currentSettingsWindow(); w != nil {
			w.setAutoPersistError(message)
		}
	})
}

func (a *App) currentSettingsWindow() *SettingsWindow {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.settingsWindow
}

// effectiveAutoState is the state the tray shows: the daemon status while
// connected, otherwise auto-enabled and the toggle chord from auto.conf.
func effectiveAutoState(st ipc.ClientStatus, cfg *userconfig.Config) ipc.ClientStatus {
	if st.Connected {
		return st
	}
	offline := ipc.ClientStatus{}
	if cfg != nil {
		offline.AutoEnabled = cfg.Settings.AutoEnabled
		offline.ToggleChord = cfg.Settings.ToggleChord.String()
	} else {
		offline.ToggleChord = userconfig.DefaultSettings().ToggleChord.String()
	}
	return offline
}

// publishAutoState pushes the current state to the tray menu and the
// settings window. It never blocks. It is called from several goroutines
// (IPC callbacks, the config watcher, toggles), so the queued updates read
// the state when they run instead of capturing a copy: an update queued
// with an older copy could otherwise be applied after a newer one and stay
// on screen.
func (a *App) publishAutoState() {
	a.mu.Lock()
	tray := a.tray
	a.mu.Unlock()
	if tray != nil {
		tray.UpdateAutoState(a.effectiveAutoStateNow)
	}
	scheduleGTK(func() {
		if w := a.currentSettingsWindow(); w != nil {
			w.setDaemonStatus(a.daemonStatus())
		}
	})
}

// effectiveAutoStateNow returns effectiveAutoState of the latest daemon
// status and user configuration.
func (a *App) effectiveAutoStateNow() ipc.ClientStatus {
	a.mu.Lock()
	st, cfg := a.autoStatus, a.userConfig
	a.mu.Unlock()
	return effectiveAutoState(st, cfg)
}

// ToggleAuto flips automatic correction: through the daemon while it is
// connected (the tray then persists the change on its behalf), otherwise by
// writing auto-enabled to auto.conf (the daemon reads it at start, or through
// its watcher when it runs without IPC).
func (a *App) ToggleAuto() {
	if err := a.toggleAuto(); err != nil {
		fmt.Printf("Warning: %s: %v\n", tr(strGAErrorToggle), err)
	}
}

func (a *App) toggleAuto() error {
	a.mu.Lock()
	dc := a.daemon
	dir := a.userDir
	a.mu.Unlock()
	if dc != nil {
		if st := dc.client.Status(); st.Connected {
			return a.SetAutoEnabled(!st.AutoEnabled)
		}
	}
	if dir == "" {
		return errors.New("no user configuration directory")
	}
	cfg, err := userconfig.Load(dir)
	if err != nil {
		return err
	}
	return a.SetAutoEnabled(!cfg.Settings.AutoEnabled)
}

// SetAutoEnabled sets automatic correction to enabled (see ToggleAuto).
func (a *App) SetAutoEnabled(enabled bool) error {
	a.mu.Lock()
	dc := a.daemon
	dir := a.userDir
	a.mu.Unlock()
	if dc != nil && dc.client.Status().Connected {
		ctx, cancel := context.WithTimeout(context.Background(), daemonCallTimeout)
		err := dc.client.SetAutoEnabled(ctx, enabled)
		cancel()
		if !errors.Is(err, ipc.ErrNotConnected) {
			return err
		}
		// The daemon vanished meanwhile: fall back to the file.
	}
	if dir == "" {
		return errors.New("no user configuration directory")
	}
	if err := userconfig.SetAutoEnabled(dir, enabled); err != nil {
		return err
	}
	// The watcher reports the write as well; refresh now for a snappy menu.
	if cfg, err := userconfig.Load(dir); err == nil {
		a.mu.Lock()
		a.userConfig = cfg
		a.mu.Unlock()
	}
	a.publishAutoState()
	return nil
}

// daemonPersister writes the daemon's runtime state on its behalf (D2: the
// daemon cannot write under $HOME). It is a single goroutine fed latest-wins
// by offer: AutoEnabledUnsaved ⇒ SetAutoEnabled(AutoEnabled);
// PendingLearnedWords ⇒ AppendListEntry for each word. The daemon's watcher
// then clears the flag and drops the words, which ends the retries.
type daemonPersister struct {
	dir            string
	setAutoEnabled func(dir string, enabled bool) error
	appendWord     func(dir, name, entry string) (bool, error)
	report         func(message string) // "" after recovery; persister goroutine
	afterPass      func(ipc.ClientStatus)
	now            func() time.Time
	skipWindow     time.Duration
	retryDelay     time.Duration

	mu      sync.Mutex
	latest  ipc.ClientStatus
	running bool
	wake    chan struct{}
	quit    chan struct{}
	done    chan struct{}

	// Owned by the persister goroutine.
	autoWritten   bool
	autoValue     bool
	autoWrittenAt time.Time
	wordsWritten  map[string]time.Time
	failing       string
}

func newDaemonPersister(dir string, report func(string)) *daemonPersister {
	return &daemonPersister{
		dir:            dir,
		setAutoEnabled: userconfig.SetAutoEnabled,
		appendWord:     userconfig.AppendListEntry,
		report:         report,
		now:            time.Now,
		skipWindow:     persistSkipWindow,
		retryDelay:     persistRetryDelay,
		wake:           make(chan struct{}, 1),
		quit:           make(chan struct{}),
		done:           make(chan struct{}),
		wordsWritten:   make(map[string]time.Time),
	}
}

// offer records the latest status; it never blocks. Only the newest status
// is processed when several arrive while a write is in progress.
func (p *daemonPersister) offer(st ipc.ClientStatus) {
	st.PendingLearnedWords = slices.Clone(st.PendingLearnedWords)
	p.mu.Lock()
	p.latest = st
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *daemonPersister) start() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running {
		return
	}
	p.running = true
	go p.run()
}

func (p *daemonPersister) stop() {
	p.mu.Lock()
	running := p.running
	p.running = false
	p.mu.Unlock()
	if !running {
		return
	}
	close(p.quit)
	<-p.done
}

func (p *daemonPersister) run() {
	defer close(p.done)
	var timer *time.Timer
	var retry <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-p.quit:
			return
		case <-p.wake:
		case <-retry:
		}
		p.mu.Lock()
		st := p.latest
		p.mu.Unlock()
		again := p.persist(st)
		if timer != nil {
			timer.Stop()
		}
		retry = nil
		if again {
			timer = time.NewTimer(p.retryDelay)
			retry = timer.C
		}
		if p.afterPass != nil {
			p.afterPass(st)
		}
	}
}

// persist performs the writes a status asks for and reports whether a retry
// is due (state still unsaved or a write failed).
func (p *daemonPersister) persist(st ipc.ClientStatus) bool {
	if !st.Connected || p.dir == "" {
		return false
	}
	now := p.now()
	var errs []error
	again := false
	if st.AutoEnabledUnsaved {
		again = true
		recent := p.autoWritten && p.autoValue == st.AutoEnabled && now.Sub(p.autoWrittenAt) < p.skipWindow
		if !recent {
			if err := p.setAutoEnabled(p.dir, st.AutoEnabled); err != nil {
				errs = append(errs, err)
			} else {
				p.autoWritten, p.autoValue, p.autoWrittenAt = true, st.AutoEnabled, now
			}
		}
	}
	for _, word := range st.PendingLearnedWords {
		again = true
		if at, ok := p.wordsWritten[word]; ok && now.Sub(at) < p.skipWindow {
			continue
		}
		if _, err := p.appendWord(p.dir, userconfig.WordsName, word); err != nil {
			errs = append(errs, err)
			continue
		}
		p.wordsWritten[word] = now
	}
	for word, at := range p.wordsWritten {
		if now.Sub(at) >= p.skipWindow && !slices.Contains(st.PendingLearnedWords, word) {
			delete(p.wordsWritten, word)
		}
	}
	p.reportResult(errors.Join(errs...))
	return again
}

func (p *daemonPersister) reportResult(err error) {
	message := ""
	if err != nil {
		message = fmt.Sprintf(tr(strGAErrorPersist), err.Error())
	}
	if message == p.failing {
		return
	}
	p.failing = message
	if p.report != nil {
		p.report(message)
	}
}

// hotkeySuspender applies the latest requested hotkey suspension state over
// D-Bus on its own goroutine, so the GTK thread never waits for the daemon.
type hotkeySuspender struct {
	call func(ctx context.Context, suspend bool) error

	mu      sync.Mutex
	want    bool
	applied bool
	running bool
	wake    chan struct{}
	quit    chan struct{}
	done    chan struct{}
}

func newHotkeySuspender(call func(ctx context.Context, suspend bool) error) *hotkeySuspender {
	return &hotkeySuspender{
		call: call,
		wake: make(chan struct{}, 1),
		quit: make(chan struct{}),
		done: make(chan struct{}),
	}
}

// request records the wanted state; it never blocks.
func (h *hotkeySuspender) request(suspend bool) {
	h.mu.Lock()
	h.want = suspend
	h.mu.Unlock()
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

func (h *hotkeySuspender) start() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.running {
		return
	}
	h.running = true
	go h.run()
}

func (h *hotkeySuspender) stop() {
	h.mu.Lock()
	running := h.running
	h.running = false
	h.mu.Unlock()
	if !running {
		return
	}
	close(h.quit)
	<-h.done
}

func (h *hotkeySuspender) run() {
	defer close(h.done)
	for {
		select {
		case <-h.quit:
			return
		case <-h.wake:
		}
		for {
			h.mu.Lock()
			want, applied := h.want, h.applied
			h.mu.Unlock()
			if want == applied {
				break
			}
			ctx, cancel := context.WithTimeout(context.Background(), daemonCallTimeout)
			err := h.call(ctx, want)
			cancel()
			if err != nil && !errors.Is(err, ipc.ErrNotConnected) {
				fmt.Printf("Warning: failed to suspend hotkeys (%t): %v\n", want, err)
			}
			// The daemon ends a suspension on its own after 120 s or when
			// this connection closes, so a failed call is not retried.
			h.mu.Lock()
			h.applied = want
			h.mu.Unlock()
		}
	}
}
