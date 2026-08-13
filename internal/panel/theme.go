package panel

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// The names and the colour format are herdr's, because the panel reads herdr's
// own configuration file: a hex value such as "#cba6f7", or an ANSI number such
// as "4", and an empty value keeps the terminal's colour. Rename a field, and
// the theme of the session stops reaching the panel.
type Theme struct {
	Accent   string
	PanelBg  string
	Surface0 string
	Surface1 string
	Overlay0 string
	Overlay1 string
	Text     string
	Subtext0 string
	Green    string
	Yellow   string
	Red      string
	Blue     string
	Teal     string
	Mauve    string
	Peach    string
}

func terminalTheme() Theme {
	return Theme{
		Accent:   "4",
		Surface1: "8",
		Overlay0: "8",
		Overlay1: "7",
		Subtext0: "7",
		Green:    "2",
		Yellow:   "3",
		Red:      "9",
		Blue:     "4",
		Teal:     "6",
		Mauve:    "5",
		Peach:    "3",
	}
}

var themes = map[string]Theme{
	"catppuccin": {
		Accent: "#89b4fa", PanelBg: "#181825", Surface0: "#313244", Surface1: "#45475a",
		Overlay0: "#6c7086", Overlay1: "#7f849c", Text: "#cdd6f4", Subtext0: "#a6adc8",
		Green: "#a6e3a1", Yellow: "#f9e2af", Red: "#f38ba8", Blue: "#89b4fa",
		Teal: "#94e2d5", Mauve: "#cba6f7", Peach: "#fab387",
	},
	"catppuccinlatte": {
		Accent: "#1e66f5", PanelBg: "#eff1f5", Surface0: "#ccd0da", Surface1: "#bcc0cc",
		Overlay0: "#9ca0b0", Overlay1: "#8c8fa1", Text: "#4c4f69", Subtext0: "#6c6f85",
		Green: "#40a02b", Yellow: "#df8e1d", Red: "#d20f39", Blue: "#1e66f5",
		Teal: "#179299", Mauve: "#8839ef", Peach: "#fe640b",
	},
	"tokyonight": {
		Accent: "#7aa2f7", PanelBg: "#1a1b26", Surface0: "#24283b", Surface1: "#414868",
		Overlay0: "#565f89", Overlay1: "#697196", Text: "#c0caf5", Subtext0: "#a9b1d6",
		Green: "#9ece6a", Yellow: "#e0af68", Red: "#f7768e", Blue: "#7aa2f7",
		Teal: "#7dcfff", Mauve: "#bb9af7", Peach: "#ff9e64",
	},
	"dracula": {
		Accent: "#bd93f9", PanelBg: "#282a36", Surface0: "#44475a", Surface1: "#6272a4",
		Overlay0: "#6272a4", Overlay1: "#828cb4", Text: "#f8f8f2", Subtext0: "#d2d2dc",
		Green: "#50fa7b", Yellow: "#f1fa8c", Red: "#ff5555", Blue: "#8be9fd",
		Teal: "#8be9fd", Mauve: "#ff79c6", Peach: "#ffb86c",
	},
	"nord": {
		Accent: "#88c0d0", PanelBg: "#2e3440", Surface0: "#3b4252", Surface1: "#434c5e",
		Overlay0: "#4c566a", Overlay1: "#646e82", Text: "#eceff4", Subtext0: "#d8dee9",
		Green: "#a3be8c", Yellow: "#ebcb8b", Red: "#bf616a", Blue: "#81a1c1",
		Teal: "#8fbcbb", Mauve: "#b48ead", Peach: "#d08770",
	},
	"gruvbox": {
		Accent: "#d79921", PanelBg: "#282828", Surface0: "#3c3836", Surface1: "#504945",
		Overlay0: "#928374", Overlay1: "#a89984", Text: "#ebdbb2", Subtext0: "#d5c4a1",
		Green: "#b8bb26", Yellow: "#fabd2f", Red: "#fb4934", Blue: "#83a598",
		Teal: "#8ec07c", Mauve: "#d3869b", Peach: "#fe8019",
	},
	"onedark": {
		Accent: "#61afef", PanelBg: "#282c34", Surface0: "#2c313a", Surface1: "#3e4451",
		Overlay0: "#5c6370", Overlay1: "#737a87", Text: "#abb2bf", Subtext0: "#969ca8",
		Green: "#98c379", Yellow: "#e5c07b", Red: "#e06c75", Blue: "#61afef",
		Teal: "#56b6c2", Mauve: "#c678dd", Peach: "#d19a66",
	},
	"rosepine": {
		Accent: "#c4a7e7", PanelBg: "#191724", Surface0: "#1f1d2e", Surface1: "#26233a",
		Overlay0: "#6e6a86", Overlay1: "#908caa", Text: "#e0def4", Subtext0: "#c8c5dc",
		Green: "#31748f", Yellow: "#f6c177", Red: "#eb6f92", Blue: "#31748f",
		Teal: "#9ccfd8", Mauve: "#c4a7e7", Peach: "#ea9a97",
	},
	"kanagawa": {
		Accent: "#7e9cd8", PanelBg: "#1f1f28", Surface0: "#2a2a37", Surface1: "#363646",
		Overlay0: "#727169", Overlay1: "#87867d", Text: "#dcd7ba", Subtext0: "#c8c3aa",
		Green: "#76946a", Yellow: "#c0a36e", Red: "#c34043", Blue: "#7e9cd8",
		Teal: "#7fb4ca", Mauve: "#957fb8", Peach: "#ffa066",
	},
}

var aliases = map[string]string{
	"catppuccinmocha": "catppuccin",
	"latte":           "catppuccinlatte",
	"light":           "catppuccinlatte",
	"gruvboxdark":     "gruvbox",
	"tokyonightday":   "catppuccinlatte",
	"onelight":        "catppuccinlatte",
	"solarizedlight":  "catppuccinlatte",
	"rosepinedawn":    "catppuccinlatte",
}

func LoadTheme() Theme {
	data, err := os.ReadFile(herdrConfigPath())
	if err != nil {
		return terminalTheme()
	}
	return themeFromConfig(data)
}

func themeFromConfig(data []byte) Theme {
	var cfg struct {
		Theme struct {
			Name   string            `toml:"name"`
			Custom map[string]string `toml:"custom"`
		} `toml:"theme"`
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return terminalTheme()
	}

	theme := terminalTheme()
	if named, ok := themes[normalizeThemeName(cfg.Theme.Name)]; ok {
		theme = named
	}
	for key, value := range cfg.Theme.Custom {
		theme.set(key, strings.TrimSpace(value))
	}
	return theme
}

func (t *Theme) set(key, value string) {
	if value == "" {
		return
	}
	switch key {
	case "accent":
		t.Accent = value
	case "panel_bg":
		t.PanelBg = value
	case "surface0":
		t.Surface0 = value
	case "surface1":
		t.Surface1 = value
	case "overlay0":
		t.Overlay0 = value
	case "overlay1":
		t.Overlay1 = value
	case "text":
		t.Text = value
	case "subtext0":
		t.Subtext0 = value
	case "green":
		t.Green = value
	case "yellow":
		t.Yellow = value
	case "red":
		t.Red = value
	case "blue":
		t.Blue = value
	case "teal":
		t.Teal = value
	case "mauve":
		t.Mauve = value
	case "peach":
		t.Peach = value
	}
}

func normalizeThemeName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	resolved := b.String()
	if alias, ok := aliases[resolved]; ok {
		return alias
	}
	return resolved
}

func herdrConfigPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "herdr", "config.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "herdr", "config.toml")
}
