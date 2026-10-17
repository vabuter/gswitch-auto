package layoutstate

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/arumata/gswitch/internal/gswitch/detect"
)

// GSettingsResync reads the current GNOME input source from gsettings
// (org.gnome.desktop.input-sources mru-sources, falling back to the first entry
// of sources when mru-sources is empty). gnome-shell writes mru-sources only on
// interactive switches, so the result carries Settings confidence only.
type GSettingsResync struct {
	env *detect.SessionEnv
	run func(env *detect.SessionEnv, args ...string) ([]byte, error)
}

var _ Resyncer = (*GSettingsResync)(nil)

// NewGSettingsResync creates a resyncer that runs gsettings for env (nil = the
// current process environment and identity).
func NewGSettingsResync(env *detect.SessionEnv) *GSettingsResync {
	return &GSettingsResync{env: env, run: detect.RunGsettings}
}

// Current returns the first input source of mru-sources (or of sources when
// mru-sources is empty or unreadable). A non-XKB first source is returned as a
// Layout with an empty Name, which maps to no pair member.
func (g *GSettingsResync) Current(ctx context.Context) (Layout, error) {
	if err := ctx.Err(); err != nil {
		return Layout{}, err
	}
	var errs []error
	for _, key := range []string{"mru-sources", "sources"} {
		output, err := g.run(g.env, "get", "org.gnome.desktop.input-sources", key)
		if err != nil {
			errs = append(errs, fmt.Errorf("gsettings get %s: %w", key, err))
			continue
		}
		if layout, ok := firstInputSource(strings.TrimSpace(string(output))); ok {
			return layout, nil
		}
		errs = append(errs, fmt.Errorf("gsettings %s: no input source", key))
		if err := ctx.Err(); err != nil {
			return Layout{}, err
		}
	}
	return Layout{}, errors.Join(errs...)
}

// inputSourceRe matches tuples like ('xkb', 'ru') or ('ibus', 'libpinyin'),
// modelled on runtime.parseGnomeInputSources.
var inputSourceRe = regexp.MustCompile(`\('([a-z]+)',\s*'([^']+)'\)`)

// firstInputSource parses the first tuple of a gsettings a(ss) value such as
// "[('xkb', 'us+dvorak'), ('xkb', 'ru')]". The XKB id encodes the variant
// after '+'.
func firstInputSource(value string) (Layout, bool) {
	m := inputSourceRe.FindStringSubmatch(value)
	if m == nil {
		return Layout{}, false
	}
	if m[1] != "xkb" {
		return Layout{}, true
	}
	name, variant, _ := strings.Cut(m[2], "+")
	return Layout{Name: name, Variant: variant}, true
}
