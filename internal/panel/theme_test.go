package panel

import "testing"

// The panel sits in a herdr pane, beside herdr's own UI. A palette that ignores
// the [theme.custom] block draws a different red from the one the user set two
// lines above, in the same file.
func TestACustomColourBeatsTheNamedTheme(t *testing.T) {
	cfg := []byte(`
[theme]
name = "catppuccin"

[theme.custom]
red = "#ff1744"
accent = "#00e5ff"
`)
	theme := themeFromConfig(cfg)

	if theme.Red != "#ff1744" {
		t.Errorf("Red = %q, want the custom colour", theme.Red)
	}
	if theme.Accent != "#00e5ff" {
		t.Errorf("Accent = %q, want the custom colour", theme.Accent)
	}
	if theme.Green != "#a6e3a1" {
		t.Errorf("Green = %q, want catppuccin's green: no custom colour replaced it", theme.Green)
	}
}

func TestAThemeNameIsReadWithoutItsSpacingOrCase(t *testing.T) {
	for _, name := range []string{"Tokyo Night", "tokyo-night", "tokyonight"} {
		theme := themeFromConfig([]byte("[theme]\nname = \"" + name + "\"\n"))
		if theme.PanelBg != "#1a1b26" {
			t.Errorf("%q gave PanelBg %q, want tokyo night's", name, theme.PanelBg)
		}
	}
}

// herdr adds themes. The panel must stay readable under a name it never heard
// of, and the terminal's own colours are the ones the user already chose.
func TestAnUnknownThemeFallsBackToTheTerminalColours(t *testing.T) {
	theme := themeFromConfig([]byte(`[theme]
name = "something-herdr-added-later"`))

	if theme != terminalTheme() {
		t.Errorf("theme = %+v, want the terminal palette", theme)
	}
}

func TestAFileWithNoThemeBlockStillDraws(t *testing.T) {
	if theme := themeFromConfig([]byte("[keys]\nprefix = \"ctrl+space\"\n")); theme != terminalTheme() {
		t.Errorf("theme = %+v, want the terminal palette", theme)
	}
	if theme := themeFromConfig([]byte("this is not toml")); theme != terminalTheme() {
		t.Errorf("theme = %+v, want the terminal palette", theme)
	}
}
