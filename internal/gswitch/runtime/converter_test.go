package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Key codes for tests
const (
	testKeyA          = 30
	testKeyB          = 48
	testKeyC          = 46
	testKeySpace      = 57
	testKeyEnter      = 28
	testKeyBackspace  = 14
	testKeyLeftShift  = 42
	testKeyRightShift = 54
	testKeyPause      = 119 // Example convert key
)

func TestConverter_NewConverter(t *testing.T) {
	c := NewConverter()
	if c == nil {
		t.Fatal("NewConverter returned nil")
	}
	if len(c.buffer) != 0 {
		t.Errorf("expected empty buffer, got %d elements", len(c.buffer))
	}
}

func TestConverter_Push_RegularKeys(t *testing.T) {
	c := NewConverter()

	// Push a regular key down
	modified := c.Push(testKeyA, K_DOWN)
	if !modified {
		t.Error("Push should return true for regular key")
	}
	if c.BufferLen() != 1 {
		t.Errorf("expected buffer length 1, got %d", c.BufferLen())
	}

	// Push key up should not add to buffer for regular keys
	modified = c.Push(testKeyA, K_UP)
	if modified {
		t.Error("Push should return false for regular key up")
	}

	// Repeat should NOT add to buffer (prevents duplicate chars on key hold)
	modified = c.Push(testKeyA, K_REPEAT)
	if modified {
		t.Error("Push should return false for regular key repeat")
	}
	if c.BufferLen() != 1 {
		t.Errorf("expected buffer length 1 after repeat, got %d", c.BufferLen())
	}
}

func TestConverter_Push_ShiftKeys(t *testing.T) {
	c := NewConverter()

	// Shift down
	modified := c.Push(testKeyLeftShift, K_DOWN)
	if !modified {
		t.Error("Push should return true for shift down")
	}

	// Shift up
	modified = c.Push(testKeyLeftShift, K_UP)
	if !modified {
		t.Error("Push should return true for shift up")
	}

	// Shift repeat should be ignored
	c.ClearBuffer()
	modified = c.Push(testKeyLeftShift, K_REPEAT)
	if modified {
		t.Error("Push should return false for shift repeat")
	}
}

func TestConverter_Push_Backspace(t *testing.T) {
	c := NewConverter()

	// Add some keys
	c.Push(testKeyA, K_DOWN)
	c.Push(testKeyB, K_DOWN)
	c.Push(testKeyC, K_DOWN)

	if c.BufferLen() != 3 {
		t.Errorf("expected buffer length 3, got %d", c.BufferLen())
	}

	// Backspace should remove last key
	c.Push(testKeyBackspace, K_DOWN)
	if c.BufferLen() != 2 {
		t.Errorf("expected buffer length 2 after backspace, got %d", c.BufferLen())
	}
}

func TestConverter_Push_KillerKeys(t *testing.T) {
	c := NewConverter()

	// Add some keys
	c.Push(testKeyA, K_DOWN)
	c.Push(testKeyB, K_DOWN)

	if c.BufferLen() != 2 {
		t.Errorf("expected buffer length 2, got %d", c.BufferLen())
	}

	// Tab (killer key) should clear buffer
	c.Push(15, K_DOWN) // KEY_TAB = 15
	if c.BufferLen() != 0 {
		t.Errorf("expected buffer to be cleared, got %d", c.BufferLen())
	}
}

func TestConverter_Push_BufferLimit(t *testing.T) {
	c := NewConverter()

	// Fill buffer beyond MaxKeyBufSize
	for range MaxKeyBufSize + 100 {
		c.Push(testKeyA, K_DOWN)
	}

	// Buffer should be trimmed
	if c.BufferLen() > MaxKeyBufSize {
		t.Errorf("buffer exceeded MaxKeyBufSize: got %d", c.BufferLen())
	}
}

func TestConverter_HasText(t *testing.T) {
	c := NewConverter()

	// Empty buffer
	if c.HasText() {
		t.Error("empty buffer should have no text")
	}

	// Only shift keys
	c.Push(testKeyLeftShift, K_DOWN)
	c.Push(testKeyLeftShift, K_UP)
	if c.HasText() {
		t.Error("buffer with only shift keys should have no text")
	}

	// With regular key
	c.Push(testKeyA, K_DOWN)
	if !c.HasText() {
		t.Error("buffer with regular key should have text")
	}
}

func TestConverter_ClearBuffer(t *testing.T) {
	c := NewConverter()

	c.Push(testKeyA, K_DOWN)
	c.Push(testKeyB, K_DOWN)

	c.ClearBuffer()
	if c.BufferLen() != 0 {
		t.Errorf("buffer should be empty after clear, got %d", c.BufferLen())
	}
}

func TestConverter_Process_DoubleShift_ConvertWord(t *testing.T) {
	c := NewConverter()
	c.ConvKey = 0 // double-shift mode

	// Type some text
	c.Push(testKeyA, K_DOWN)
	c.Push(testKeyB, K_DOWN)
	c.Push(testKeyC, K_DOWN)

	// Double-shift pattern: shift down, up, down, up (same shift)
	c.Push(testKeyLeftShift, K_DOWN)
	c.Push(testKeyLeftShift, K_UP)
	c.Push(testKeyLeftShift, K_DOWN)
	c.Push(testKeyLeftShift, K_UP)

	action := c.Process()
	if action != ActionConvertWord {
		t.Errorf("expected ActionConvertWord, got %v", action)
	}
}

func TestConverter_Process_DoubleShift_ConvertAll(t *testing.T) {
	c := NewConverter()
	c.ConvKey = 0 // double-shift mode

	// Type some text
	c.Push(testKeyA, K_DOWN)
	c.Push(testKeyB, K_DOWN)
	c.Push(testKeyC, K_DOWN)

	// Hold one shift, double-tap the other
	// Pattern: shift1 down, shift2 down, shift2 up, shift2 down, shift2 up, shift1 up
	c.Push(testKeyLeftShift, K_DOWN)
	c.Push(testKeyRightShift, K_DOWN)
	c.Push(testKeyRightShift, K_UP)
	c.Push(testKeyRightShift, K_DOWN)
	c.Push(testKeyRightShift, K_UP)
	c.Push(testKeyLeftShift, K_UP)

	action := c.Process()
	if action != ActionConvertAll {
		t.Errorf("expected ActionConvertAll, got %v", action)
	}
}

func TestConverter_Process_DoubleShift_NoText(t *testing.T) {
	c := NewConverter()
	c.ConvKey = 0 // double-shift mode

	// Double-shift without text: no buffer conversion, but the trigger
	// itself must be reported so Ctrl+DoubleShift can convert the selection
	c.Push(testKeyLeftShift, K_DOWN)
	c.Push(testKeyLeftShift, K_UP)
	c.Push(testKeyLeftShift, K_DOWN)
	c.Push(testKeyLeftShift, K_UP)

	action := c.Process()
	if action != ActionDoubleShiftNoText {
		t.Errorf("expected ActionDoubleShiftNoText for double-shift without text, got %v", action)
	}
	if len(c.buffer) != 0 {
		t.Errorf("expected buffer to be trimmed, got %d events", len(c.buffer))
	}
}

func TestConverter_Process_DoubleShift_NoText_StrayShiftPrefix(t *testing.T) {
	c := NewConverter()
	c.ConvKey = 0 // double-shift mode

	// A single aborted shift tap left in the buffer must not mask the trigger
	c.Push(testKeyLeftShift, K_DOWN)
	c.Push(testKeyLeftShift, K_UP)
	if got := c.Process(); got != ActionNone {
		t.Fatalf("expected ActionNone after single shift tap, got %v", got)
	}

	c.Push(testKeyLeftShift, K_DOWN)
	c.Push(testKeyLeftShift, K_UP)
	c.Push(testKeyLeftShift, K_DOWN)
	c.Push(testKeyLeftShift, K_UP)

	action := c.Process()
	if action != ActionDoubleShiftNoText {
		t.Errorf("expected ActionDoubleShiftNoText with stray shift prefix, got %v", action)
	}
}

func TestConverter_Process_CustomKey_ConvertWord(t *testing.T) {
	c := NewConverter()
	c.ConvKey = testKeyPause // custom convert key

	// Type some text
	c.Push(testKeyA, K_DOWN)
	c.Push(testKeyB, K_DOWN)

	// Press convert key without shift
	c.Push(testKeyPause, K_DOWN)
	c.Push(testKeyPause, K_UP)

	action := c.Process()
	if action != ActionConvertWord {
		t.Errorf("expected ActionConvertWord, got %v", action)
	}
}

func TestConverter_Process_CustomKey_ConvertAll(t *testing.T) {
	c := NewConverter()
	c.ConvKey = testKeyPause // custom convert key

	// Type some text
	c.Push(testKeyA, K_DOWN)
	c.Push(testKeyB, K_DOWN)

	// Press convert key with shift
	c.Push(testKeyLeftShift, K_DOWN)
	c.Push(testKeyPause, K_DOWN)
	c.Push(testKeyPause, K_UP)
	c.Push(testKeyLeftShift, K_UP)

	action := c.Process()
	if action != ActionConvertAll {
		t.Errorf("expected ActionConvertAll, got %v", action)
	}
}

func TestConverter_Process_CustomKey_ShiftHeldFromTyping_DoesNotDropShiftUp(t *testing.T) {
	c := NewConverter()
	c.ConvKey = testKeyPause
	c.LSKeys = []uint16{125}

	// Simulate typing with Shift held, then pressing ConvKey while Shift is still held.
	// Conversion may trigger before we observe shift-up; we must still ensure replay releases shift
	// to avoid "stuck shift" and garbage output.
	c.Push(testKeyLeftShift, K_DOWN)
	c.Push(testKeyA, K_DOWN)
	c.Push(testKeyPause, K_DOWN)
	c.Push(testKeyPause, K_UP)

	action := c.Process()
	if action != ActionConvertWord {
		t.Fatalf("expected ActionConvertWord, got %v", action)
	}

	// ConvKey events should be trimmed out
	for _, ev := range c.GetBuffer() {
		if ev.Code == testKeyPause {
			t.Fatalf("expected convkey events to be trimmed from buffer, found: %+v", ev)
		}
	}

	// Shift up must remain to avoid shift being stuck during replay
	events := c.Convert(action)
	for _, ev := range events {
		if ev.Code == testKeyPause {
			t.Fatalf("expected convkey not to be replayed, found: %+v", ev)
		}
	}

	// Even though we never pushed shift-up, Convert() should ensure shift is released in output.
	hasShiftUpInOutput := false
	for _, ev := range events {
		if ev.Code == testKeyLeftShift && ev.Value == K_UP {
			hasShiftUpInOutput = true
			break
		}
	}
	if !hasShiftUpInOutput {
		t.Fatal("expected Convert() to release shift in output events")
	}
}

func TestConverter_Convert_GeneratesBackspaces(t *testing.T) {
	c := NewConverter()
	c.LSKeys = []uint16{125} // layout switch key

	// Type "abc"
	c.Push(testKeyA, K_DOWN)
	c.Push(testKeyB, K_DOWN)
	c.Push(testKeyC, K_DOWN)

	events := c.Convert(ActionConvertWord)

	// Should have: layout switch + backspaces + replayed keys
	// Layout switch: 1 down + 1 up = 2
	// Backspaces for 3 chars: 3 * 2 = 6
	// Replay 3 keys: 3 * 2 = 6
	// Total: 14

	if len(events) < 14 {
		t.Errorf("expected at least 14 events, got %d", len(events))
	}

	// First events should be layout switch
	if events[0].Code != 125 || events[0].Value != K_DOWN {
		t.Error("first event should be layout switch key down")
	}
}

func TestConverter_Convert_WordVsAll(t *testing.T) {
	c := NewConverter()
	c.LSKeys = []uint16{125}

	// Type "hello world" (with space)
	for _, key := range []uint16{35, 18, 38, 38, 24} { // h e l
		c.Push(key, K_DOWN)
	}
	c.Push(testKeySpace, K_DOWN)
	for _, key := range []uint16{17, 24, 19, 38, 32} { // w o r l d
		c.Push(key, K_DOWN)
	}

	// ConvertWord should only convert "world"
	wordEvents := c.Convert(ActionConvertWord)

	c.ClearBuffer()
	// Refill buffer
	for _, key := range []uint16{35, 18, 38, 38, 24} {
		c.Push(key, K_DOWN)
	}
	c.Push(testKeySpace, K_DOWN)
	for _, key := range []uint16{17, 24, 19, 38, 32} {
		c.Push(key, K_DOWN)
	}

	// ConvertAll should convert everything
	allEvents := c.Convert(ActionConvertAll)

	// ConvertAll should have more events than ConvertWord
	if len(allEvents) <= len(wordEvents) {
		t.Errorf("ConvertAll should produce more events than ConvertWord: %d vs %d",
			len(allEvents), len(wordEvents))
	}
}

func TestConverterDebugOutputOmitsBufferContent(t *testing.T) {
	c := NewConverter()
	var messages []string
	c.SetDebugLogger(func(format string, args ...any) {
		messages = append(messages, fmt.Sprintf(format, args...))
	})

	c.Push(testKeyA, K_DOWN)
	c.Push(testKeyLeftShift, K_DOWN)
	c.Push(testKeyLeftShift, K_UP)
	c.Push(testKeyLeftShift, K_DOWN)
	c.Push(testKeyLeftShift, K_UP)
	if got := c.Process(); got != ActionConvertWord {
		t.Fatalf("Process() = %v, want ActionConvertWord", got)
	}
	c.Convert(ActionConvertWord)

	output := strings.Join(messages, "\n")
	for _, sensitive := range []string{"buffer before trim:", "buffer after trim:", "replay sequence (", "A_DOWN"} {
		if strings.Contains(output, sensitive) {
			t.Fatalf("converter debug output exposes buffer content %q: %q", sensitive, output)
		}
	}
}

func TestConverter_BackspaceRemovesShiftPairs(t *testing.T) {
	c := NewConverter()

	// Type shifted character: shift down, a, shift up
	c.Push(testKeyLeftShift, K_DOWN)
	c.Push(testKeyA, K_DOWN)
	c.Push(testKeyLeftShift, K_UP)

	initialLen := c.BufferLen()

	// Backspace should remove the key and trailing shift pair
	c.Push(testKeyBackspace, K_DOWN)

	// Should have removed the key and possibly shift pair
	if c.BufferLen() >= initialLen {
		t.Error("backspace should reduce buffer length")
	}
}

func TestConverter_DifferentShiftKeys(t *testing.T) {
	c := NewConverter()
	c.ConvKey = 0

	// Type text
	c.Push(testKeyA, K_DOWN)

	// Double-tap with mixed shifts should NOT trigger (needs same shift)
	c.Push(testKeyLeftShift, K_DOWN)
	c.Push(testKeyLeftShift, K_UP)
	c.Push(testKeyRightShift, K_DOWN)
	c.Push(testKeyRightShift, K_UP)

	action := c.Process()
	if action == ActionConvertWord {
		t.Error("mixed shift double-tap should not trigger convert")
	}
}

func TestConverter_RepeatedTriggerUndoesLastConversion(t *testing.T) {
	tests := []struct {
		name    string
		convKey uint16
		want    Action
		trigger []KeyEvent
	}{
		{
			name: "double shift word",
			want: ActionConvertWord,
			trigger: []KeyEvent{
				{Code: testKeyLeftShift, Value: K_DOWN},
				{Code: testKeyLeftShift, Value: K_UP},
				{Code: testKeyLeftShift, Value: K_DOWN},
				{Code: testKeyLeftShift, Value: K_UP},
			},
		},
		{
			name: "double shift phrase",
			want: ActionConvertAll,
			trigger: []KeyEvent{
				{Code: testKeyLeftShift, Value: K_DOWN},
				{Code: testKeyRightShift, Value: K_DOWN},
				{Code: testKeyRightShift, Value: K_UP},
				{Code: testKeyRightShift, Value: K_DOWN},
				{Code: testKeyRightShift, Value: K_UP},
				{Code: testKeyLeftShift, Value: K_UP},
			},
		},
		{
			name:    "custom key word",
			convKey: testKeyPause,
			want:    ActionConvertWord,
			trigger: []KeyEvent{
				{Code: testKeyPause, Value: K_DOWN},
				{Code: testKeyPause, Value: K_UP},
			},
		},
		{
			name:    "custom key phrase",
			convKey: testKeyPause,
			want:    ActionConvertAll,
			trigger: []KeyEvent{
				{Code: testKeyLeftShift, Value: K_DOWN},
				{Code: testKeyPause, Value: K_DOWN},
				{Code: testKeyPause, Value: K_UP},
				{Code: testKeyLeftShift, Value: K_UP},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewConverter()
			c.ConvKey = tt.convKey
			c.LSKeys = []uint16{125}
			c.Push(testKeyA, K_DOWN)
			c.Push(testKeyB, K_DOWN)

			for _, event := range tt.trigger {
				c.Push(event.Code, event.Value)
			}
			firstAction := c.Process()
			if firstAction != tt.want {
				t.Fatalf("first Process() = %v, want %v", firstAction, tt.want)
			}
			firstEvents := c.Convert(firstAction)
			if wasUndo := c.CompleteConversion(firstAction); wasUndo {
				t.Fatal("first conversion must not be classified as undo")
			}

			for _, event := range tt.trigger {
				c.Push(event.Code, event.Value)
			}
			secondAction := c.Process()
			if secondAction != tt.want {
				t.Fatalf("second Process() = %v, want %v", secondAction, tt.want)
			}
			if !c.CanUndo(secondAction) {
				t.Fatal("same trigger immediately after conversion must be eligible for undo")
			}
			secondEvents := c.Convert(secondAction)
			if !reflect.DeepEqual(secondEvents, firstEvents) {
				t.Fatalf("undo events differ from conversion events:\nfirst:  %#v\nsecond: %#v", firstEvents, secondEvents)
			}
			if wasUndo := c.CompleteConversion(secondAction); !wasUndo {
				t.Fatal("second conversion must be classified as undo")
			}
			if c.CanUndo(secondAction) {
				t.Fatal("completed undo must consume one-step undo state")
			}
		})
	}
}

func TestConverter_ContentInputInvalidatesUndo(t *testing.T) {
	c := NewConverter()
	c.LSKeys = []uint16{125}
	c.Push(testKeyA, K_DOWN)
	c.CompleteConversion(ActionConvertWord)

	if !c.CanUndo(ActionConvertWord) {
		t.Fatal("completed conversion must arm undo")
	}
	c.Push(testKeyB, K_DOWN)
	if c.CanUndo(ActionConvertWord) {
		t.Fatal("content input must invalidate undo")
	}
}

func TestConverter_Process_DoubleShiftHeld_NoText(t *testing.T) {
	c := NewConverter()

	for _, event := range []KeyEvent{
		{Code: testKeyLeftShift, Value: K_DOWN},
		{Code: testKeyRightShift, Value: K_DOWN},
		{Code: testKeyRightShift, Value: K_UP},
		{Code: testKeyRightShift, Value: K_DOWN},
		{Code: testKeyRightShift, Value: K_UP},
		{Code: testKeyLeftShift, Value: K_UP},
	} {
		c.Push(event.Code, event.Value)
	}

	if got := c.Process(); got != ActionDoubleShiftHeldNoText {
		t.Fatalf("Process() = %v, want ActionDoubleShiftHeldNoText", got)
	}
}

// gswitch-auto WP0: golden capture of Convert output. The values were
// captured from the v0.8.0 converter before Convert was split into the
// start-index scan and ConvertRange; they must never change.

type emitTestConvertFixture struct {
	name    string
	lsKeys  []uint16
	convKey uint16
	script  string // emitTestSeq script pushed into the converter
	process bool   // run Process (and its trimBuffer) before converting
}

var emitTestConvertFixtures = []emitTestConvertFixture{
	{name: "empty", lsKeys: []uint16{125}},
	{name: "shift-only", lsKeys: []uint16{125}, script: "ls+ ls-"},
	{name: "abc", lsKeys: []uint16{125}, script: "a b c"},
	{name: "hello-world", lsKeys: []uint16{125}, script: "h e l l o sp w o r l d"},
	{name: "trailing-space", lsKeys: []uint16{125}, script: "a b sp"},
	{name: "double-spaces", lsKeys: []uint16{125}, script: "a sp sp b sp sp"},
	{name: "space-only", lsKeys: []uint16{125}, script: "sp"},
	{name: "enter-boundary", lsKeys: []uint16{125}, script: "a b ent c d"},
	{name: "kpenter-boundary", lsKeys: []uint16{125}, script: "a kpent b"},
	{name: "trailing-enter", lsKeys: []uint16{125}, script: "a b ent"},
	{name: "line-then-words", lsKeys: []uint16{125}, script: "a sp b ent c sp d"},
	{name: "shifted-word", lsKeys: []uint16{125}, script: "ls+ a b ls- c"},
	{name: "capitalised-word-space", lsKeys: []uint16{125}, script: "ls+ g ls- h b sp"},
	{name: "shift-held-at-end", lsKeys: []uint16{125}, script: "a ls+ b"},
	{name: "both-shifts-held-at-end", lsKeys: []uint16{125}, script: "a ls+ rs+ b"},
	{name: "right-shift-across-space", lsKeys: []uint16{125}, script: "rs+ a sp b rs-"},
	{name: "shift-only-tail", lsKeys: []uint16{125}, script: "a ls+ sp ls- ls+ ls- ls+ ls-", process: true},
	{name: "double-shift-word", lsKeys: []uint16{125}, script: "a b ls+ ls- ls+ ls-", process: true},
	{name: "held-shift-phrase", lsKeys: []uint16{125}, script: "a b sp c ls+ rs+ rs- rs+ rs- ls-", process: true},
	{name: "mixed-shift-taps", lsKeys: []uint16{125}, script: "a ls+ ls- rs+ rs-", process: true},
	{name: "custom-key-word", lsKeys: []uint16{125}, convKey: testKeyPause, script: "a b pause+ pause-", process: true},
	{name: "custom-key-phrase", lsKeys: []uint16{125}, convKey: testKeyPause, script: "a b ls+ pause+ pause- ls-", process: true},
	{name: "custom-key-shift-held", lsKeys: []uint16{125}, convKey: testKeyPause, script: "ls+ a pause+ pause-", process: true},
	{name: "custom-key-mid-buffer", lsKeys: []uint16{125}, convKey: testKeyPause, script: "a pause+ pause- b"},
	{name: "backspace-edit", lsKeys: []uint16{125}, script: "a b c bs d"},
	{name: "backspace-shift-pair", lsKeys: []uint16{125}, script: "ls+ a ls- bs b"},
	{name: "repeat-ignored", lsKeys: []uint16{125}, script: "a a~ b"},
	{name: "killer-clears", lsKeys: []uint16{125}, script: "a b tab c"},
	{name: "ctrl-not-buffered", lsKeys: []uint16{125}, script: "a ctrl+ b ctrl-"},
	{name: "digits-punctuation", lsKeys: []uint16{125}, script: "1 2 , . /"},
	{name: "keypad-keys", lsKeys: []uint16{125}, script: "#71 #72 #55 #98"},
	{name: "alt-shift-toggle", lsKeys: []uint16{56, 42}, script: "a b sp c d"},
	{name: "no-toggle-keys", script: "a b"},
}

// emitTestConvertRecords returns, per fixture, the Process result and the
// canonicalised Convert output for ActionConvertWord, ActionConvertAll and
// ActionNone, plus every debug line the converter logged.
func emitTestConvertRecords(t *testing.T) (map[string]string, []string) {
	t.Helper()
	got := make(map[string]string)
	var logs []string
	for _, fx := range emitTestConvertFixtures {
		c := NewConverter()
		c.ConvKey = fx.convKey
		c.LSKeys = fx.lsKeys
		c.SetDebugLogger(func(format string, args ...any) {
			logs = append(logs, fx.name+": "+fmt.Sprintf(format, args...))
		})
		for _, ev := range emitTestSeq(t, fx.script) {
			c.Push(ev.Code, ev.Value)
		}
		processed := "-"
		if fx.process {
			processed = emitTestActionName(c.Process())
		}
		before := emitTestFormat(c.GetBuffer())
		for _, action := range []Action{ActionConvertWord, ActionConvertAll, ActionNone} {
			out := emitTestFormat(emitTestCanonicalConvert(c.Convert(action)))
			got[fmt.Sprintf("%s/process=%s/%s", fx.name, processed, emitTestActionName(action))] = out
		}
		if after := emitTestFormat(c.GetBuffer()); after != before {
			t.Errorf("%s: Convert mutated the buffer: %s -> %s", fx.name, before, after)
		}
	}
	return got, logs
}

func TestConverterConvertGolden(t *testing.T) {
	got, logs := emitTestConvertRecords(t)
	emitTestCompareGolden(t, got, emitTestConvertGolden)

	sum := sha256.Sum256([]byte(strings.Join(logs, "\n")))
	if digest := hex.EncodeToString(sum[:]); digest != emitTestConvertLogDigest {
		t.Errorf("converter debug log digest = %s, want %s; log:\n%s", digest, emitTestConvertLogDigest, strings.Join(logs, "\n"))
	}
}

const emitTestConvertLogDigest = "3f147b61e4263b848d8fdbec8831a5e78708b6938950783ea99b4268480d4003"

var emitTestConvertGolden = map[string]string{
	"abc/process=-/all":                       "125+ 125- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 48+ 48- 46+ 46-",
	"abc/process=-/none":                      "125+ 125- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 48+ 48- 46+ 46-",
	"abc/process=-/word":                      "125+ 125- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 48+ 48- 46+ 46-",
	"alt-shift-toggle/process=-/all":          "56+ 42+ 42- 56- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 48+ 48- 57+ 57- 46+ 46- 32+ 32-",
	"alt-shift-toggle/process=-/none":         "56+ 42+ 42- 56- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 48+ 48- 57+ 57- 46+ 46- 32+ 32-",
	"alt-shift-toggle/process=-/word":         "56+ 42+ 42- 56- 14+ 14- 14+ 14- 46+ 46- 32+ 32-",
	"backspace-edit/process=-/all":            "125+ 125- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 48+ 48- 32+ 32-",
	"backspace-edit/process=-/none":           "125+ 125- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 48+ 48- 32+ 32-",
	"backspace-edit/process=-/word":           "125+ 125- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 48+ 48- 32+ 32-",
	"backspace-shift-pair/process=-/all":      "125+ 125- 14+ 14- 48+ 48-",
	"backspace-shift-pair/process=-/none":     "125+ 125- 14+ 14- 48+ 48-",
	"backspace-shift-pair/process=-/word":     "125+ 125- 14+ 14- 48+ 48-",
	"both-shifts-held-at-end/process=-/all":   "125+ 125- 14+ 14- 14+ 14- 30+ 30- 42+ 54+ 48+ 48- 42- 54-",
	"both-shifts-held-at-end/process=-/none":  "125+ 125- 14+ 14- 14+ 14- 30+ 30- 42+ 54+ 48+ 48- 42- 54-",
	"both-shifts-held-at-end/process=-/word":  "125+ 125- 14+ 14- 14+ 14- 30+ 30- 42+ 54+ 48+ 48- 42- 54-",
	"capitalised-word-space/process=-/all":    "125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 42+ 34+ 34- 42- 35+ 35- 48+ 48- 57+ 57-",
	"capitalised-word-space/process=-/none":   "125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 42+ 34+ 34- 42- 35+ 35- 48+ 48- 57+ 57-",
	"capitalised-word-space/process=-/word":   "125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 42+ 34+ 34- 42- 35+ 35- 48+ 48- 57+ 57-",
	"ctrl-not-buffered/process=-/all":         "125+ 125- 14+ 14- 14+ 14- 30+ 30- 48+ 48-",
	"ctrl-not-buffered/process=-/none":        "125+ 125- 14+ 14- 14+ 14- 30+ 30- 48+ 48-",
	"ctrl-not-buffered/process=-/word":        "125+ 125- 14+ 14- 14+ 14- 30+ 30- 48+ 48-",
	"custom-key-mid-buffer/process=-/all":     "125+ 125- 14+ 14- 14+ 14- 30+ 30- 48+ 48-",
	"custom-key-mid-buffer/process=-/none":    "125+ 125- 14+ 14- 14+ 14- 30+ 30- 48+ 48-",
	"custom-key-mid-buffer/process=-/word":    "125+ 125- 14+ 14- 14+ 14- 30+ 30- 48+ 48-",
	"custom-key-phrase/process=all/all":       "125+ 125- 14+ 14- 14+ 14- 30+ 30- 48+ 48-",
	"custom-key-phrase/process=all/none":      "125+ 125- 14+ 14- 14+ 14- 30+ 30- 48+ 48-",
	"custom-key-phrase/process=all/word":      "125+ 125- 14+ 14- 14+ 14- 30+ 30- 48+ 48-",
	"custom-key-shift-held/process=word/all":  "125+ 125- 14+ 14- 42+ 30+ 30- 42-",
	"custom-key-shift-held/process=word/none": "125+ 125- 14+ 14- 42+ 30+ 30- 42-",
	"custom-key-shift-held/process=word/word": "125+ 125- 14+ 14- 42+ 30+ 30- 42-",
	"custom-key-word/process=word/all":        "125+ 125- 14+ 14- 14+ 14- 30+ 30- 48+ 48-",
	"custom-key-word/process=word/none":       "125+ 125- 14+ 14- 14+ 14- 30+ 30- 48+ 48-",
	"custom-key-word/process=word/word":       "125+ 125- 14+ 14- 14+ 14- 30+ 30- 48+ 48-",
	"digits-punctuation/process=-/all":        "125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 2+ 2- 3+ 3- 51+ 51- 52+ 52- 53+ 53-",
	"digits-punctuation/process=-/none":       "125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 2+ 2- 3+ 3- 51+ 51- 52+ 52- 53+ 53-",
	"digits-punctuation/process=-/word":       "125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 2+ 2- 3+ 3- 51+ 51- 52+ 52- 53+ 53-",
	"double-shift-word/process=word/all":      "125+ 125- 14+ 14- 14+ 14- 30+ 30- 48+ 48-",
	"double-shift-word/process=word/none":     "125+ 125- 14+ 14- 14+ 14- 30+ 30- 48+ 48-",
	"double-shift-word/process=word/word":     "125+ 125- 14+ 14- 14+ 14- 30+ 30- 48+ 48-",
	"double-spaces/process=-/all":             "125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 57+ 57- 57+ 57- 48+ 48- 57+ 57- 57+ 57-",
	"double-spaces/process=-/none":            "125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 57+ 57- 57+ 57- 48+ 48- 57+ 57- 57+ 57-",
	"double-spaces/process=-/word":            "125+ 125- 14+ 14- 14+ 14- 14+ 14- 48+ 48- 57+ 57- 57+ 57-",
	"empty/process=-/all":                     "<nil>",
	"empty/process=-/none":                    "<nil>",
	"empty/process=-/word":                    "<nil>",
	"enter-boundary/process=-/all":            "125+ 125- 14+ 14- 14+ 14- 46+ 46- 32+ 32-",
	"enter-boundary/process=-/none":           "125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 48+ 48- 28+ 28- 46+ 46- 32+ 32-",
	"enter-boundary/process=-/word":           "125+ 125- 14+ 14- 14+ 14- 46+ 46- 32+ 32-",
	"held-shift-phrase/process=all/all":       "125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 48+ 48- 57+ 57- 46+ 46-",
	"held-shift-phrase/process=all/none":      "125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 48+ 48- 57+ 57- 46+ 46-",
	"held-shift-phrase/process=all/word":      "125+ 125- 14+ 14- 46+ 46-",
	"hello-world/process=-/all":               "125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 35+ 35- 18+ 18- 38+ 38- 38+ 38- 24+ 24- 57+ 57- 17+ 17- 24+ 24- 19+ 19- 38+ 38- 32+ 32-",
	"hello-world/process=-/none":              "125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 35+ 35- 18+ 18- 38+ 38- 38+ 38- 24+ 24- 57+ 57- 17+ 17- 24+ 24- 19+ 19- 38+ 38- 32+ 32-",
	"hello-world/process=-/word":              "125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 17+ 17- 24+ 24- 19+ 19- 38+ 38- 32+ 32-",
	"keypad-keys/process=-/all":               "125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 71+ 71- 72+ 72- 55+ 55- 98+ 98-",
	"keypad-keys/process=-/none":              "125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 71+ 71- 72+ 72- 55+ 55- 98+ 98-",
	"keypad-keys/process=-/word":              "125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 71+ 71- 72+ 72- 55+ 55- 98+ 98-",
	"killer-clears/process=-/all":             "125+ 125- 14+ 14- 46+ 46-",
	"killer-clears/process=-/none":            "125+ 125- 14+ 14- 46+ 46-",
	"killer-clears/process=-/word":            "125+ 125- 14+ 14- 46+ 46-",
	"kpenter-boundary/process=-/all":          "125+ 125- 14+ 14- 48+ 48-",
	"kpenter-boundary/process=-/none":         "125+ 125- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 96+ 96- 48+ 48-",
	"kpenter-boundary/process=-/word":         "125+ 125- 14+ 14- 48+ 48-",
	"line-then-words/process=-/all":           "125+ 125- 14+ 14- 14+ 14- 14+ 14- 46+ 46- 57+ 57- 32+ 32-",
	"line-then-words/process=-/none":          "125+ 125- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 57+ 57- 48+ 48- 28+ 28- 46+ 46- 57+ 57- 32+ 32-",
	"line-then-words/process=-/word":          "125+ 125- 14+ 14- 32+ 32-",
	"mixed-shift-taps/process=none/all":       "125+ 125- 14+ 14- 30+ 30- 42+ 42- 54+ 54-",
	"mixed-shift-taps/process=none/none":      "125+ 125- 14+ 14- 30+ 30- 42+ 42- 54+ 54-",
	"mixed-shift-taps/process=none/word":      "125+ 125- 14+ 14- 30+ 30- 42+ 42- 54+ 54-",
	"no-toggle-keys/process=-/all":            "14+ 14- 14+ 14- 30+ 30- 48+ 48-",
	"no-toggle-keys/process=-/none":           "14+ 14- 14+ 14- 30+ 30- 48+ 48-",
	"no-toggle-keys/process=-/word":           "14+ 14- 14+ 14- 30+ 30- 48+ 48-",
	"repeat-ignored/process=-/all":            "125+ 125- 14+ 14- 14+ 14- 30+ 30- 48+ 48-",
	"repeat-ignored/process=-/none":           "125+ 125- 14+ 14- 14+ 14- 30+ 30- 48+ 48-",
	"repeat-ignored/process=-/word":           "125+ 125- 14+ 14- 14+ 14- 30+ 30- 48+ 48-",
	"right-shift-across-space/process=-/all":  "125+ 125- 14+ 14- 14+ 14- 14+ 14- 54+ 30+ 30- 57+ 57- 48+ 48- 54-",
	"right-shift-across-space/process=-/none": "125+ 125- 14+ 14- 14+ 14- 14+ 14- 54+ 30+ 30- 57+ 57- 48+ 48- 54-",
	"right-shift-across-space/process=-/word": "125+ 125- 14+ 14- 48+ 48- 54-",
	"shift-held-at-end/process=-/all":         "125+ 125- 14+ 14- 14+ 14- 30+ 30- 42+ 48+ 48- 42-",
	"shift-held-at-end/process=-/none":        "125+ 125- 14+ 14- 14+ 14- 30+ 30- 42+ 48+ 48- 42-",
	"shift-held-at-end/process=-/word":        "125+ 125- 14+ 14- 14+ 14- 30+ 30- 42+ 48+ 48- 42-",
	"shift-only-tail/process=word/all":        "125+ 125- 14+ 14- 14+ 14- 30+ 30- 42+ 57+ 57- 42-",
	"shift-only-tail/process=word/none":       "125+ 125- 14+ 14- 14+ 14- 30+ 30- 42+ 57+ 57- 42-",
	"shift-only-tail/process=word/word":       "125+ 125- 42-",
	"shift-only/process=-/all":                "<nil>",
	"shift-only/process=-/none":               "<nil>",
	"shift-only/process=-/word":               "<nil>",
	"shifted-word/process=-/all":              "125+ 125- 14+ 14- 14+ 14- 14+ 14- 42+ 30+ 30- 48+ 48- 42- 46+ 46-",
	"shifted-word/process=-/none":             "125+ 125- 14+ 14- 14+ 14- 14+ 14- 42+ 30+ 30- 48+ 48- 42- 46+ 46-",
	"shifted-word/process=-/word":             "125+ 125- 14+ 14- 14+ 14- 14+ 14- 42+ 30+ 30- 48+ 48- 42- 46+ 46-",
	"space-only/process=-/all":                "125+ 125- 14+ 14- 57+ 57-",
	"space-only/process=-/none":               "125+ 125- 14+ 14- 57+ 57-",
	"space-only/process=-/word":               "125+ 125- 14+ 14- 57+ 57-",
	"trailing-enter/process=-/all":            "125+ 125- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 48+ 48- 28+ 28-",
	"trailing-enter/process=-/none":           "125+ 125- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 48+ 48- 28+ 28-",
	"trailing-enter/process=-/word":           "125+ 125- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 48+ 48- 28+ 28-",
	"trailing-space/process=-/all":            "125+ 125- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 48+ 48- 57+ 57-",
	"trailing-space/process=-/none":           "125+ 125- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 48+ 48- 57+ 57-",
	"trailing-space/process=-/word":           "125+ 125- 14+ 14- 14+ 14- 14+ 14- 30+ 30- 48+ 48- 57+ 57-",
}

func emitTestConverter(t *testing.T, script string) *Converter {
	t.Helper()
	c := NewConverter()
	c.LSKeys = []uint16{125}
	for _, ev := range emitTestSeq(t, script) {
		c.Push(ev.Code, ev.Value)
	}
	return c
}

func TestConverterConvertRangeInvalidBounds(t *testing.T) {
	c := emitTestConverter(t, "a b c")
	for _, r := range [][2]int{{-1, 2}, {0, 4}, {2, 2}, {3, 1}, {3, 3}, {-1, -1}, {4, 5}} {
		if got := c.ConvertRange(r[0], r[1]); got != nil {
			t.Errorf("ConvertRange(%d, %d) = %s, want nil", r[0], r[1], emitTestFormat(got))
		}
	}
	if got := NewConverter().ConvertRange(0, 0); got != nil {
		t.Errorf("ConvertRange(0, 0) on an empty buffer = %s, want nil", emitTestFormat(got))
	}
}

func TestConverterConvertRangeShiftOnlyRange(t *testing.T) {
	c := emitTestConverter(t, "a ls+ sp ls-")
	if got, want := emitTestFormat(c.ConvertRange(3, 4)), "125+ 125- 42-"; got != want {
		t.Errorf("ConvertRange(3, 4) = %s, want %s (toggle plus the replayed shift release)", got, want)
	}
	if got, want := emitTestFormat(c.ConvertRange(1, 2)), "125+ 125- 42+ 42-"; got != want {
		t.Errorf("ConvertRange(1, 2) = %s, want %s (toggle, shift, extra release)", got, want)
	}
}

func TestConverterConvertWordShiftOnlyTailUsesConvertRange(t *testing.T) {
	c := emitTestConverter(t, "a ls+ sp ls- ls+ ls- ls+ ls-")
	if action := c.Process(); action != ActionConvertWord {
		t.Fatalf("Process() = %v, want ActionConvertWord", action)
	}
	if got := emitTestFormat(c.GetBuffer()); got != "30+ 42+ 57+ 42-" {
		t.Fatalf("trimmed buffer = %s", got)
	}
	word := c.Convert(ActionConvertWord)
	if !reflect.DeepEqual(word, c.ConvertRange(3, 4)) {
		t.Fatalf("Convert(word) = %s, want ConvertRange(3, 4) = %s",
			emitTestFormat(word), emitTestFormat(c.ConvertRange(3, 4)))
	}
}

func TestConverterConvertRangeWordWithTrailingSpace(t *testing.T) {
	c := emitTestConverter(t, "a b sp ls+ c ls- d sp")
	got := emitTestFormat(c.ConvertRange(3, 8))
	want := "125+ 125- 14+ 14- 14+ 14- 14+ 14- 42+ 46+ 46- 42- 32+ 32- 57+ 57-"
	if got != want {
		t.Errorf("ConvertRange(3, 8) = %s, want %s", got, want)
	}
	if all := c.ConvertRange(0, c.BufferLen()); !reflect.DeepEqual(all, c.Convert(ActionConvertAll)) {
		t.Errorf("ConvertRange(0, len) = %s, want Convert(all) = %s",
			emitTestFormat(all), emitTestFormat(c.Convert(ActionConvertAll)))
	}
}

func TestConverterUndoGeneration(t *testing.T) {
	c := NewConverter()
	c.ConvKey = testKeyPause
	c.LSKeys = []uint16{125}
	gen := c.UndoGeneration()
	step := func(name string, bump bool, fn func()) {
		t.Helper()
		fn()
		got := c.UndoGeneration()
		switch {
		case bump && got == gen:
			t.Errorf("%s: UndoGeneration stayed %d, want a bump", name, got)
		case !bump && got != gen:
			t.Errorf("%s: UndoGeneration changed %d -> %d, want unchanged", name, gen, got)
		}
		gen = got
	}
	push := func(script string) func() {
		return func() {
			for _, ev := range emitTestSeq(t, script) {
				c.Push(ev.Code, ev.Value)
			}
		}
	}

	step("text key down", true, push("a+"))
	step("text key up", false, push("a-"))
	step("text key repeat", false, push("a~"))
	step("left shift down/up", false, push("ls+ ls-"))
	step("right shift down/up", false, push("rs+ rs-"))
	step("shift repeat", false, push("ls~"))
	step("convert key", false, push("pause+ pause-"))
	step("Convert", false, func() { c.Convert(ActionConvertWord) })
	step("ConvertRange", false, func() { c.ConvertRange(0, c.BufferLen()) })
	step("Process", false, func() { c.Process() })
	step("second text key", true, push("b+"))
	step("backspace down", true, push("bs+"))
	step("backspace repeat", true, push("bs~"))
	step("backspace up", false, push("bs-"))
	step("arm undo", false, func() { c.CompleteConversion(ActionConvertWord) })
	step("consume undo", true, func() { c.CompleteConversion(ActionConvertWord) })
	step("ClearBuffer", true, c.ClearBuffer)
	step("killer key", true, push("tab+"))
	step("killer key up (upstream clears on release too)", true, push("tab-"))
	step("Ctrl is not a killer", false, push("ctrl+ ctrl-"))
}
