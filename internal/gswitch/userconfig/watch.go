package userconfig

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// DefaultDebounce is the quiet time after a change event before the
	// directory is reloaded.
	DefaultDebounce = 200 * time.Millisecond
	// DefaultPollInterval is the polling period without inotify and while
	// the directory is missing.
	DefaultPollInterval = 2 * time.Second

	watchMask = unix.IN_CLOSE_WRITE | unix.IN_MOVED_TO | unix.IN_MOVED_FROM | unix.IN_CREATE |
		unix.IN_DELETE | unix.IN_DELETE_SELF | unix.IN_MOVE_SELF | unix.IN_ATTRIB
	watchGoneMask = unix.IN_DELETE_SELF | unix.IN_MOVE_SELF | unix.IN_IGNORED | unix.IN_UNMOUNT
)

// stampRecheckInterval is how often the inotify watcher compares file stamps
// as a safety net (missed events, the directory replaced via a parent rename).
var stampRecheckInterval = 10 * time.Second

// testHookWatchReady, when set by tests, runs on the watcher goroutine once
// the baseline has been loaded.
var testHookWatchReady func()

// Options tunes Watch. Zero values select the defaults.
type Options struct {
	Debounce     time.Duration // DefaultDebounce
	PollInterval time.Duration // DefaultPollInterval
	ForcePoll    bool          // never use inotify
	// Baseline is the configuration the caller loaded and applied before
	// calling Watch. When it is set and the directory differs from it once
	// the watch is installed, onChange fires at once, so a change made
	// between the caller's Load and the start of the watch is not lost.
	// nil: the watcher's own initial Load is the baseline.
	Baseline *Config
}

// Watch blocks until ctx is done and then returns nil (non-nil only for
// invalid arguments). It does NOT fire an initial callback: callers Load
// first (and pass that Config as Options.Baseline to catch changes made
// before the watch started). onChange runs on the watcher goroutine, only
// when the loaded Config differs from the previous one (!Equal). The directory is watched with
// inotify (debounced) with a periodic stamp re-check; without inotify, or
// with ForcePoll, it is polled every PollInterval. A missing directory is
// polled for and watched once it appears; a removed and recreated directory
// is picked up again.
func Watch(ctx context.Context, dir string, onChange func(*Config), opts Options) error {
	if ctx == nil {
		return errors.New("userconfig: Watch needs a context")
	}
	if onChange == nil {
		return errors.New("userconfig: Watch needs a callback")
	}
	if err := checkDir(dir); err != nil {
		return err
	}
	if opts.Debounce < 0 || opts.PollInterval < 0 {
		return errors.New("userconfig: negative watch interval")
	}
	if opts.Debounce == 0 {
		opts.Debounce = DefaultDebounce
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = DefaultPollInterval
	}
	w := &dirWatcher{dir: filepath.Clean(dir), onChange: onChange, opts: opts}
	w.run(ctx)
	return nil
}

type dirWatcher struct {
	dir      string
	onChange func(*Config)
	opts     Options

	in      *inotifyWatch // nil while polling
	current *Config
	stamps  dirStamps

	debounce *time.Timer
	poll     *time.Ticker
	recheck  *time.Ticker
}

func (w *dirWatcher) run(ctx context.Context) {
	w.debounce = time.NewTimer(time.Hour)
	w.debounce.Stop()
	w.poll = time.NewTicker(w.opts.PollInterval)
	w.recheck = time.NewTicker(stampRecheckInterval)
	w.recheck.Stop() // runs only in inotify mode
	defer func() {
		w.debounce.Stop()
		w.poll.Stop()
		w.recheck.Stop()
		w.stopInotify()
	}()

	// Watch before taking the baseline so no change after it is missed.
	w.startInotify()
	w.current, _ = Load(w.dir)
	w.stamps = w.current.stamps
	if b := w.opts.Baseline; b != nil && !w.current.Equal(b) {
		// Changed since the caller's Load, before anything watched it.
		w.onChange(w.current)
	}
	if testHookWatchReady != nil {
		testHookWatchReady()
	}

	for {
		var events <-chan bool
		if w.in != nil {
			events = w.in.events
		}
		select {
		case <-ctx.Done():
			return
		case gone, ok := <-events:
			if !ok || gone {
				// The directory was removed or moved away, or the
				// reader failed: poll until it can be watched again.
				w.stopInotify()
			}
			w.debounce.Reset(w.opts.Debounce)
		case <-w.debounce.C:
			w.reload()
		case <-w.poll.C:
			w.onPoll()
		case <-w.recheck.C:
			w.onRecheck()
		}
	}
}

// startInotify switches to inotify mode when allowed and possible.
func (w *dirWatcher) startInotify() bool {
	if w.opts.ForcePoll || w.in != nil {
		return w.in != nil
	}
	in, err := newInotifyWatch(w.dir)
	if err != nil {
		return false
	}
	w.in = in
	w.poll.Stop()
	w.recheck.Reset(stampRecheckInterval)
	return true
}

// stopInotify switches to polling mode.
func (w *dirWatcher) stopInotify() {
	if w.in == nil {
		return
	}
	w.in.close()
	w.in = nil
	w.recheck.Stop()
	w.poll.Reset(w.opts.PollInterval)
}

// onPoll runs in polling mode: try to (re)establish inotify, else compare
// stamps.
func (w *dirWatcher) onPoll() {
	if w.in != nil {
		return
	}
	if w.startInotify() {
		// Files may have changed while nothing watched them.
		w.reload()
		return
	}
	if readStamps(w.dir) != w.stamps {
		w.reload()
	}
}

// onRecheck runs in inotify mode: detect a replaced directory and changes
// whose events were missed.
func (w *dirWatcher) onRecheck() {
	if w.in == nil {
		return
	}
	stamps := readStamps(w.dir)
	if d := stamps[0]; d.state != stampPresent || d.dev != w.in.dev || d.ino != w.in.ino {
		w.stopInotify()
		w.reload()
		return
	}
	if stamps != w.stamps {
		w.reload()
	}
}

func (w *dirWatcher) reload() {
	cfg, _ := Load(w.dir)
	w.stamps = cfg.stamps
	if cfg.Equal(w.current) {
		return
	}
	w.current = cfg
	w.onChange(cfg)
}

// inotifyWatch is one inotify instance watching the configuration directory.
// A reader goroutine sends true on events when the directory itself is gone,
// false for a relevant change; it closes events when reading fails.
type inotifyWatch struct {
	f        *os.File
	dev, ino uint64
	events   chan bool
	done     chan struct{}
	wg       sync.WaitGroup
}

func newInotifyWatch(dir string) (*inotifyWatch, error) {
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		return nil, err
	}
	if _, err := unix.InotifyAddWatch(fd, dir, watchMask|unix.IN_ONLYDIR); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	var st unix.Stat_t
	if err := unix.Stat(dir, &st); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	// A non-blocking fd gives a pollable *os.File: Close unblocks Read.
	in := &inotifyWatch{
		f:      os.NewFile(uintptr(fd), "inotify:"+dir), //nolint:gosec // fd is a valid descriptor
		dev:    st.Dev,
		ino:    st.Ino,
		events: make(chan bool, 1),
		done:   make(chan struct{}),
	}
	in.wg.Add(1)
	go in.read()
	return in, nil
}

func (in *inotifyWatch) read() {
	defer in.wg.Done()
	defer close(in.events)
	buf := make([]byte, 16*(unix.SizeofInotifyEvent+unix.NAME_MAX+1))
	for {
		n, err := in.f.Read(buf)
		if err != nil {
			return
		}
		relevant, gone := parseInotifyEvents(buf[:n])
		if !relevant && !gone {
			continue
		}
		select {
		case in.events <- gone:
		case <-in.done:
			return
		}
	}
}

func (in *inotifyWatch) close() {
	close(in.done)
	_ = in.f.Close()
	in.wg.Wait()
}

// parseInotifyEvents reports whether buf holds an event about one of the
// configuration files (or the directory's own attributes, or a queue
// overflow) and whether the directory itself went away.
func parseInotifyEvents(buf []byte) (relevant, gone bool) {
	for off := 0; off+unix.SizeofInotifyEvent <= len(buf); {
		mask := binary.NativeEndian.Uint32(buf[off+4:])
		nameLen := int(binary.NativeEndian.Uint32(buf[off+12:]))
		start := off + unix.SizeofInotifyEvent
		end := min(start+nameLen, len(buf))
		name := buf[start:end]
		if i := bytes.IndexByte(name, 0); i >= 0 {
			name = name[:i]
		}
		off = end
		switch {
		case mask&watchGoneMask != 0:
			gone = true
		case mask&unix.IN_Q_OVERFLOW != 0, len(name) == 0:
			relevant = true
		case slices.Contains(watchedNames[:], string(name)):
			relevant = true
		}
	}
	return relevant, gone
}
