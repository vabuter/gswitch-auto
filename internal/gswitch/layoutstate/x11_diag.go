package layoutstate

import (
	"context"
	"io"
	"time"
)

// RunX11Diagnostics prints the display, the XKB extension opcode and version,
// the _XKB_RULES_NAMES value and the group table derived from it, the current
// group with its engine name and pair index, then, when watch is positive,
// streams tracker state changes for that long (gswitch --layout-state on X11;
// the manual test script watches Super+Space switches with it). Everything is
// read-only. It returns an error when the display or the XKB extension cannot
// be reached; the information gathered so far has been printed by then.
func RunX11Diagnostics(ctx context.Context, w io.Writer, display string, layouts [2]Layout, watch time.Duration) error {
	pw := &diagWriter{w: w}
	pw.printf("Layout pair: %s / %s\n", layouts[0], layouts[1])
	pw.printf("Display: %s\n", x11DisplayName(display))

	source := NewX11Source(X11Options{
		Display:  display,
		Fallback: layouts,
		Logf:     func(format string, args ...any) { pw.printf(format+"\n", args...) },
	})
	sess, err := source.connect(ctx, false)
	if err != nil {
		pw.printf("X11: error: %v\n", err)
		return err
	}
	pw.printf("XKB extension: opcode %d, version %d.%d\n", sess.opcode, sess.major, sess.minor)
	if sess.rules == nil {
		pw.printf("%s: missing\n", xkbRulesNamesProperty)
	} else {
		f := xkbRulesNamesFields(sess.rules)
		pw.printf("%s: rules %q, model %q, layouts %q, variants %q, options %q\n",
			xkbRulesNamesProperty, f[0], f[1], f[2], f[3], f[4])
	}
	if sess.fromRules {
		pw.printf("Layout table: %s\n", formatLayouts(sess.table))
	} else {
		pw.printf("Layout table: %s (configured pair; %s is unusable)\n", formatLayouts(sess.table), xkbRulesNamesProperty)
	}
	group, err := source.getState(ctx, sess)
	sess.conn.Close()
	if err != nil {
		err = source.describeErr(ctx, err)
		pw.printf("XKB group: error: %v\n", err)
		return err
	}
	pw.printf("XKB group: %d\n", group)
	engine, ok := EngineForGroup(sess.table, int(group))
	if ok {
		pw.printf("Engine: %q\n", engine)
	} else {
		pw.printf("Engine: none (group %d is outside the table)\n", group)
	}
	pw.printf("Mapped: %s\n", describeEngine(layouts, engine))
	if watch <= 0 {
		return pw.error()
	}
	return watchX11Diagnostics(ctx, pw, source, layouts, watch)
}

// watchX11Diagnostics is watchDiagnostics for the X11 source: it runs a tracker
// on source for watch and prints every state change.
func watchX11Diagnostics(ctx context.Context, pw *diagWriter, source Source, layouts [2]Layout, watch time.Duration) error {
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
