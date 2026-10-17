package layoutstate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arumata/gswitch/internal/gswitch/detect"
)

// gsettingsFake returns a run func that answers "get <schema> <key>" from values.
func gsettingsFake(values map[string]string, errs map[string]error, calls *[]string) func(*detect.SessionEnv, ...string) ([]byte, error) {
	return func(_ *detect.SessionEnv, args ...string) ([]byte, error) {
		*calls = append(*calls, strings.Join(args, " "))
		key := args[len(args)-1]
		if err := errs[key]; err != nil {
			return nil, err
		}
		return []byte(values[key] + "\n"), nil
	}
}

func TestGSettingsResyncCurrent(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
		errs   map[string]error
		want   Layout
		calls  int
	}{
		{
			name:   "mru first entry",
			values: map[string]string{"mru-sources": "[('xkb', 'ru'), ('xkb', 'us')]"},
			want:   Layout{Name: "ru"},
			calls:  1,
		},
		{
			name:   "variant",
			values: map[string]string{"mru-sources": "[('xkb', 'us+dvorak'), ('xkb', 'ru')]"},
			want:   Layout{Name: "us", Variant: "dvorak"},
			calls:  1,
		},
		{
			name:   "empty mru falls back to sources",
			values: map[string]string{"mru-sources": "@a(ss) []", "sources": "[('xkb', 'us'), ('xkb', 'ru')]"},
			want:   Layout{Name: "us"},
			calls:  2,
		},
		{
			name:   "mru error falls back to sources",
			values: map[string]string{"sources": "[('xkb', 'ru'), ('xkb', 'us')]"},
			errs:   map[string]error{"mru-sources": errors.New("no schema")},
			want:   Layout{Name: "ru"},
			calls:  2,
		},
		{
			name:   "non-xkb first source",
			values: map[string]string{"mru-sources": "[('ibus', 'libpinyin'), ('xkb', 'us')]"},
			want:   Layout{},
			calls:  1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			g := NewGSettingsResync(nil)
			g.run = gsettingsFake(tt.values, tt.errs, &calls)
			got, err := g.Current(context.Background())
			if err != nil || got != tt.want {
				t.Fatalf("Current() = %+v, %v; want %+v", got, err, tt.want)
			}
			if len(calls) != tt.calls || calls[0] != "get org.gnome.desktop.input-sources mru-sources" {
				t.Fatalf("gsettings calls = %v", calls)
			}
		})
	}
}

func TestGSettingsResyncErrors(t *testing.T) {
	var calls []string
	g := NewGSettingsResync(nil)
	g.run = gsettingsFake(map[string]string{"mru-sources": "@a(ss) []", "sources": "@a(ss) []"}, nil, &calls)
	if _, err := g.Current(context.Background()); err == nil {
		t.Fatal("Current() with no sources: error = nil")
	}

	g.run = gsettingsFake(nil, map[string]error{"mru-sources": errors.New("a"), "sources": errors.New("b")}, &calls)
	if _, err := g.Current(context.Background()); err == nil {
		t.Fatal("Current() with failing gsettings: error = nil")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls = nil
	if _, err := g.Current(ctx); !errors.Is(err, context.Canceled) || len(calls) != 0 {
		t.Fatalf("Current(cancelled) = %v with %d calls, want context.Canceled and no gsettings", err, len(calls))
	}
}

func TestGSettingsResyncInTracker(t *testing.T) {
	var calls []string
	g := NewGSettingsResync(nil)
	g.run = gsettingsFake(map[string]string{"mru-sources": "[('xkb', 'us'), ('xkb', 'ru')]"}, nil, &calls)
	tr, _ := newTestTracker(usRu)
	tr.AddResync(g)
	if err := tr.Resync(context.Background()); err != nil {
		t.Fatalf("Resync() error = %v", err)
	}
	st := tr.State()
	if st.Confidence != Settings || st.Index != 0 || st.Source != SourceGSettings {
		t.Fatalf("state = %+v, want Settings index 0", st)
	}
	if _, known := tr.Current(); known {
		t.Fatal("gsettings-only state reported as known")
	}
}
