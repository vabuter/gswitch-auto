package layoutstate

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

// diagQueryTimeout bounds the one-shot engine read of RunDiagnostics.
const diagQueryTimeout = 2 * time.Second

// RunDiagnostics prints the IBus socket path (address file), the resolved address and
// daemon pid, the current global engine and its pair index, then, when watch is
// positive, streams tracker state changes for that long (gswitch
// --layout-state [--watch SECONDS]). It returns an error when IBus cannot be
// reached; the information gathered so far has been printed by then.
func RunDiagnostics(ctx context.Context, w io.Writer, env AddressEnv, layouts [2]Layout, watch time.Duration) error {
	pw := &diagWriter{w: w}
	pw.printf("Layout pair: %s / %s\n", layouts[0], layouts[1])
	if path, err := SocketPath(env); err != nil {
		pw.printf("IBus socket path: error: %v\n", err)
	} else {
		pw.printf("IBus socket path: %s\n", path)
	}
	if env.IBusAddress != "" {
		pw.printf("IBUS_ADDRESS: set in environment (address file not used)\n")
	}

	source := NewIBusSource(env, nil)
	source.connectSession = nil
	addr, pid, err := source.resolve(env)
	if err != nil {
		pw.printf("IBus address: error: %v\n", err)
		return err
	}
	pw.printf("IBus address: %s\n", addr)
	if pid > 0 {
		pw.printf("IBus daemon pid: %d\n", pid)
	}

	queryCtx, cancel := context.WithTimeout(ctx, diagQueryTimeout)
	engine, err := source.Query(queryCtx)
	cancel()
	if err != nil {
		pw.printf("Global engine: error: %v\n", err)
		return err
	}
	pw.printf("Global engine: %q\n", engine)
	pw.printf("Mapped: %s\n", describeEngine(layouts, engine))
	if watch <= 0 {
		return pw.error()
	}
	return watchDiagnostics(ctx, pw, source, layouts, watch)
}

func watchDiagnostics(ctx context.Context, pw *diagWriter, source *IBusSource, layouts [2]Layout, watch time.Duration) error {
	pw.printf("Watching layout changes for %v...\n", watch)
	tracker := New(Options{Layouts: layouts})
	tracker.AddSource(source)
	tracker.OnChange(func(st State) {
		pw.printf("%s %s\n", st.At.Format("15:04:05.000"), st)
	})
	watchCtx, cancel := context.WithTimeout(ctx, watch)
	defer cancel()
	tracker.Start(watchCtx)
	<-watchCtx.Done()
	tracker.Stop()
	return pw.error()
}

// describeEngine formats the pair mapping of an engine name.
func describeEngine(layouts [2]Layout, engine string) string {
	layout, variant, ok := ParseXKBEngine(engine)
	if !ok {
		return "not an XKB engine (index -1, confidence unknown)"
	}
	l := Layout{Name: layout, Variant: variant}
	idx := pairIndex(layouts, l)
	if idx < 0 {
		return fmt.Sprintf("layout %s is not one of the pair (index -1, confidence unknown)", l)
	}
	return fmt.Sprintf("layout %s = index %d (confidence confirmed)", l, idx)
}

// diagWriter remembers the first write error and serialises writes from the
// tracker goroutine.
type diagWriter struct {
	mu  sync.Mutex
	w   io.Writer
	err error
}

func (d *diagWriter) printf(format string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return
	}
	_, d.err = fmt.Fprintf(d.w, format, args...)
}

func (d *diagWriter) error() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}
