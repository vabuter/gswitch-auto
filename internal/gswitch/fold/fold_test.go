package fold

import "testing"

func TestWord(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"Ghbdtn,", "ghbdtn"},
		{"ЁЖИК!", "ежик"},
		{"k.,jdm", "k.,jdm"},
		{`"привет"`, "привет"},
		{"123", ""},
		{"", ""},
		{"...", ""},
		{"ёлка", "елка"},
		{"Ёлка", "елка"},
		{"Hello", "hello"},
		{"руддщ!", "руддщ"},
		{"?ghbdtn?", "ghbdtn"},
		{"«Слово»", "слово"},
		{"don't", "don't"},
		{"1abc2", "abc"},
		{" Mixed Ёж ", "mixed еж"},
	}
	for _, tt := range tests {
		if got := Word(tt.in); got != tt.want {
			t.Errorf("Word(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestAppKey(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"org.gnome.Ptyxis.desktop", "org.gnome.ptyxis"},
		{"  Kitty ", "kitty"},
		{"kitty", "kitty"},
		{"", ""},
		{"   ", ""},
		{"Firefox.DESKTOP", "firefox"},
		{"org.gnome.Terminal.desktop\n", "org.gnome.terminal"},
		{".desktop", ""},
		{"a.desktop.desktop", "a"},
		{"a .desktop", "a"},
		{"desktop", "desktop"},
		{"org.desktop.app", "org.desktop.app"},
	}
	for _, tt := range tests {
		if got := AppKey(tt.in); got != tt.want {
			t.Errorf("AppKey(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

var foldTestIdempotenceInputs = []string{
	"Ghbdtn,", "ЁЖИК!", "k.,jdm", `"привет"`, "123", "", "ёлка", "«Слово»",
	"org.gnome.Ptyxis.desktop", "  Kitty ", "a.desktop.desktop", "a .desktop",
	"Firefox.DESKTOP", " Mixed Ёж ", "İstanbul", "ΣΊΣΥΦΟΣ",
}

func TestWordIdempotent(t *testing.T) {
	for _, in := range foldTestIdempotenceInputs {
		once := Word(in)
		if twice := Word(once); twice != once {
			t.Errorf("Word(Word(%q)) = %q, want %q", in, twice, once)
		}
	}
}

func TestAppKeyIdempotent(t *testing.T) {
	for _, in := range foldTestIdempotenceInputs {
		once := AppKey(in)
		if twice := AppKey(once); twice != once {
			t.Errorf("AppKey(AppKey(%q)) = %q, want %q", in, twice, once)
		}
	}
}

func TestAppKeyDoesNotAllocateForNormalisedInput(t *testing.T) {
	for _, in := range []string{"org.gnome.ptyxis", "kitty", "", "приложение"} {
		allocs := testing.AllocsPerRun(100, func() {
			if AppKey(in) != in {
				t.Fatalf("AppKey(%q) changed a normalised value", in)
			}
		})
		if allocs != 0 {
			t.Errorf("AppKey(%q) allocated %.0f times, want 0", in, allocs)
		}
	}
}
