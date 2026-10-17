// Package i18n provides immutable, embedded interface message catalogs.
package i18n

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

//go:embed locales/*.json
var files embed.FS

// Catalog is a translation and the metadata used by the language selector.
type Catalog struct {
	Code      string            `json:"code"`
	Name      string            `json:"name"`
	Direction string            `json:"direction"`
	Messages  map[string]string `json:"messages"`
}

// Language describes an available translation in its own language.
type Language struct{ Code, Name, Direction string }

// Bundle is an immutable set of catalogs with an English fallback.
type Bundle struct{ catalogs map[string]Catalog }

// Translator is an immutable selected language, safe for concurrent readers.
type Translator struct {
	bundle *Bundle
	code   string
}

// Builtin contains the translations shipped in the binary.
var Builtin = builtin()

// ForkKeyPrefix marks message IDs added by the gswitch-auto fork. Fork
// messages are required only in ForkLocales; other catalogs fall back to
// English for them (Text).
const ForkKeyPrefix = "strGA"

// ForkLocales lists the catalogs that must translate every fork message.
var ForkLocales = []string{"en", "ru"}

// IsForkKey reports whether key is a fork message ID.
func IsForkKey(key string) bool { return strings.HasPrefix(key, ForkKeyPrefix) }

func builtin() *Bundle {
	root, err := fs.Sub(files, "locales")
	if err != nil {
		panic(err)
	}
	bundle, err := Load(root)
	if err != nil {
		panic(err)
	}
	return bundle
}

// Load validates all JSON catalogs in the root of fsys.
func Load(fsys fs.FS) (*Bundle, error) {
	names, err := fs.Glob(fsys, "*.json")
	if err != nil {
		return nil, err
	}
	b := &Bundle{catalogs: make(map[string]Catalog)}
	for _, name := range names {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		if err := uniqueJSON(data); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		var c Catalog
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if c.Code == "" || c.Code != normalize(c.Code) || c.Name == "" || (c.Direction != "ltr" && c.Direction != "rtl") {
			return nil, fmt.Errorf("%s: invalid metadata", name)
		}
		if _, exists := b.catalogs[c.Code]; exists {
			return nil, fmt.Errorf("duplicate language %s", c.Code)
		}
		b.catalogs[c.Code] = c
	}
	if len(b.catalogs["en"].Messages) == 0 {
		return nil, errors.New("english catalog is required")
	}
	for code, c := range b.catalogs {
		for key, value := range c.Messages {
			base, ok := b.catalogs["en"].Messages[key]
			if !ok || value == "" {
				return nil, fmt.Errorf("%s: unknown or empty message %s", code, key)
			}
			if err := sameParameters(base, value); err != nil {
				return nil, fmt.Errorf("%s/%s: %w", code, key, err)
			}
		}
	}
	return b, nil
}

// Languages returns catalog metadata sorted by language code.
func (b *Bundle) Languages() []Language {
	result := make([]Language, 0, len(b.catalogs))
	for _, c := range b.catalogs {
		result = append(result, Language{c.Code, c.Name, c.Direction})
	}
	slices.SortFunc(result, func(a, b Language) int { return strings.Compare(a.Code, b.Code) })
	return result
}

// Select resolves a manual choice or the GNU message-locale environment.
// LANGUAGE applies only outside C/POSIX. LC_ALL overrides LC_MESSAGES and LANG.
func (b *Bundle) Select(preference string, getenv func(string) string) *Translator {
	candidates := []string{preference}
	if preference == "" || preference == "auto" {
		locale := "C"
		for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
			if v := getenv(key); v != "" {
				locale = v
				break
			}
		}
		if code := normalize(locale); code == "c" || code == "posix" {
			return &Translator{b, "en"}
		}
		candidates = strings.Split(getenv("LANGUAGE"), ":")
		candidates = append(candidates, locale)
	}
	for _, candidate := range candidates {
		code := normalize(candidate)
		if code == "c" || code == "posix" {
			break
		}
		for code != "" {
			if _, ok := b.catalogs[code]; ok {
				return &Translator{b, code}
			}
			index := strings.LastIndexByte(code, '-')
			if index < 0 {
				break
			}
			code = code[:index]
		}
	}
	return &Translator{b, "en"}
}

func normalize(code string) string {
	code, _, _ = strings.Cut(strings.TrimSpace(code), ".")
	code, _, _ = strings.Cut(code, "@")
	return strings.ToLower(strings.ReplaceAll(code, "_", "-"))
}

// Code returns the resolved catalog code.
func (t *Translator) Code() string { return t.code }

// Direction returns ltr or rtl from catalog metadata.
func (t *Translator) Direction() string { return t.bundle.catalogs[t.code].Direction }

// Text returns a translated message, then English, then a literal (e.g. a key name).
func (t *Translator) Text(key string) string {
	if text := t.bundle.catalogs[t.code].Messages[key]; text != "" {
		return text
	}
	if text := t.bundle.catalogs["en"].Messages[key]; text != "" {
		return text
	}
	return key
}

// Format substitutes printf parameters; catalogs can reorder them with %[n].
func (t *Translator) Format(key string, args ...any) string { return fmt.Sprintf(t.Text(key), args...) }
