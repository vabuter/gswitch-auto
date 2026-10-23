package layoutstate

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/jezek/xgb"
)

// TestX11Integration talks to the X server of $DISPLAY. It only reads (XKB
// extension, _XKB_RULES_NAMES, XkbGetState) and passes against the host's
// Xwayland. With GSWITCH_X11_TEST_SWITCH=1 it additionally locks XKB group 1
// and restores group 0, which changes the server's keyboard state: run that
// variant only inside a container or under xvfb-run, never on a live desktop.
// Run with GSWITCH_X11_TEST=1.
func TestX11Integration(t *testing.T) {
	if os.Getenv("GSWITCH_X11_TEST") != "1" {
		t.Skip("set GSWITCH_X11_TEST=1 to run against $DISPLAY")
	}
	display := os.Getenv("DISPLAY")
	if display == "" {
		t.Fatal("DISPLAY is unset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	raw, err := dialX11(ctx, display)
	if err != nil {
		t.Fatalf("dialX11(%s) error = %v", display, err)
	}
	defer raw.Close()
	opcode, err := raw.QueryExtension(ctx)
	if err != nil {
		t.Fatalf("QueryExtension() error = %v", err)
	}
	major, minor, err := raw.UseExtension(ctx)
	if err != nil {
		t.Fatalf("UseExtension() error = %v", err)
	}
	data, found, err := raw.RulesNames(ctx)
	if err != nil {
		t.Fatalf("RulesNames() error = %v", err)
	}
	group, err := raw.GetState(ctx)
	if err != nil {
		t.Fatalf("GetState() error = %v", err)
	}
	t.Logf("display %s: XKB opcode %d version %d.%d, %s found=%v %q, group %d",
		display, opcode, major, minor, xkbRulesNamesProperty, found, data, group)

	table := usRu[:]
	if parsed, ok := ParseXKBRulesNames(data); found && ok && len(parsed) >= 2 {
		table = parsed
	}
	pair := [2]Layout{table[0], table[1]}
	wantEngine, _ := EngineForGroup(table, int(group))

	src := NewX11Source(X11Options{Display: display, Fallback: pair, Logf: t.Logf})
	queryCtx, queryCancel := context.WithTimeout(ctx, 2*time.Second)
	engine, err := src.Query(queryCtx)
	queryCancel()
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if engine != wantEngine {
		t.Fatalf("Query() = %q, want %q (table %s, group %d)", engine, wantEngine, formatLayouts(table), group)
	}

	tracker := New(Options{Layouts: pair, DebugLogger: t.Logf})
	tracker.AddSource(src)
	start := time.Now()
	tracker.Start(ctx)
	defer tracker.Stop()
	waitUntil(t, func() bool { return tracker.State().Source == SourceX11 })
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("tracker reported the X11 source after %v, want within 1 s", elapsed)
	}
	st := tracker.State()
	t.Logf("tracker state: %s", st)
	if st.Engine != wantEngine {
		t.Fatalf("tracker engine = %q, want %q", st.Engine, wantEngine)
	}
	if int(group) < len(table) && (st.Confidence != Confirmed || st.Index != int(group)) {
		t.Fatalf("tracker state = %s, want Confirmed index %d", st, group)
	}

	if os.Getenv("GSWITCH_X11_TEST_SWITCH") == "1" {
		lock, ok := raw.(*xgbConn)
		if !ok {
			t.Fatalf("dialX11 returned %T, want *xgbConn", raw)
		}
		if len(table) < 2 {
			t.Fatalf("switch test needs two layouts, table is %s", formatLayouts(table))
		}
		x11LayoutTestLockGroup(t, lock, 1)
		x11LayoutTestExpectIndex(t, tracker, 1, 200*time.Millisecond)
		x11LayoutTestLockGroup(t, lock, 0)
		x11LayoutTestExpectIndex(t, tracker, 0, 200*time.Millisecond)
	}

	var out bytes.Buffer
	if err := RunX11Diagnostics(ctx, &out, display, pair, 0); err != nil {
		t.Fatalf("RunX11Diagnostics() error = %v\n%s", err, out.String())
	}
	t.Logf("diagnostics:\n%s", out.String())
}

// x11LayoutTestLockGroup sends XkbLatchLockState(deviceSpec=UseCoreKbd,
// lockGroup=true, groupLock=group) on c and waits for the server to process
// it. Request layout per /usr/include/X11/extensions/XKBproto.h
// xkbLatchLockStateReq (16 bytes, length field 4; note the pad byte between
// modLatches and latchGroup).
func x11LayoutTestLockGroup(t *testing.T, c *xgbConn, group uint8) {
	t.Helper()
	request := make([]byte, 16)
	request[0] = c.opcode
	request[1] = 5 // X_kbLatchLockState
	xgb.Put16(request[2:], 4)
	xgb.Put16(request[4:], xkbUseCoreKeyboard)
	request[6] = 0 // affectModLocks
	request[7] = 0 // modLocks
	request[8] = 1 // lockGroup
	request[9] = group
	request[10] = 0 // affectModLatches
	request[11] = 0 // modLatches
	request[12] = 0 // pad
	request[13] = 0 // latchGroup
	xgb.Put16(request[14:], 0)
	cookie := c.conn.NewCookie(true, false)
	c.conn.NewRequest(request, cookie)
	if err := cookie.Check(); err != nil {
		t.Fatalf("XkbLatchLockState(group %d) error = %v", group, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := c.GetState(ctx)
	if err != nil {
		t.Fatalf("GetState() after lock error = %v", err)
	}
	if got != group {
		t.Fatalf("group after XkbLatchLockState = %d, want %d", got, group)
	}
}

// x11LayoutTestExpectIndex waits up to timeout for the tracker to confirm idx.
func x11LayoutTestExpectIndex(t *testing.T, tracker *Tracker, idx int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		st := tracker.State()
		if st.Confidence == Confirmed && st.Index == idx {
			t.Logf("tracker confirmed index %d: %s", idx, st)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("tracker state = %s, want Confirmed index %d within %v", st, idx, timeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
