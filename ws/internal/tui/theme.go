package tui

import (
	"fmt"
	"image/color"
	"math"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/igwtcode/workstation/ws/internal/config"
	"github.com/igwtcode/workstation/ws/internal/ui"
)

func DefaultPalette() config.Palette {
	return config.Palette{
		ID:   "gruvbox-dark",
		Name: "gruvbox-dark",
		Mode: "dark",
		Tokens: map[string]string{
			"name":         "gruvbox-dark",
			"mode":         "dark",
			"bg":           "#282828",
			"bg_alt":       "#3c3836",
			"fg":           "#ebdbb2",
			"fg_dim":       "#928374",
			"accent":       "#fe8019",
			"selection_bg": "#665c54",
			"selection_fg": "#ebdbb2",
			"cursor":       "#ebdbb2",
			"border":       "#504945",
			"color0":       "#282828",
			"color1":       "#cc241d",
			"color2":       "#98971a",
			"color3":       "#d79921",
			"color4":       "#458588",
			"color5":       "#b16286",
			"color6":       "#689d6a",
			"color7":       "#a89984",
			"color8":       "#928374",
			"color9":       "#fb4934",
			"color10":      "#b8bb26",
			"color11":      "#fabd2f",
			"color12":      "#83a598",
			"color13":      "#d3869b",
			"color14":      "#8ec07c",
			"color15":      "#ebdbb2",
		},
	}
}

func defaultLightTokens() map[string]string {
	return map[string]string{
		"bg":           "#fbf1c7",
		"bg_alt":       "#ebdbb2",
		"fg":           "#3c3836",
		"fg_dim":       "#928374",
		"accent":       "#af3a03",
		"selection_bg": "#bdae93",
		"selection_fg": "#3c3836",
		"border":       "#d5c4a1",
		"color1":       "#cc241d",
		"color2":       "#98971a",
		"color3":       "#d79921",
		"color4":       "#458588",
		"color5":       "#b16286",
		"color6":       "#689d6a",
		"color9":       "#9d0006",
		"color10":      "#79740e",
		"color11":      "#b57614",
		"color12":      "#076678",
		"color13":      "#8f3f71",
		"color14":      "#427b58",
	}
}

type Styles struct {
	Palette config.Palette
	ThemeID string
	IsLight bool
	Roles   ui.Roles

	Bg       color.Color
	BgAlt    color.Color
	Fg       color.Color
	FgDim    color.Color
	Accent   color.Color
	SelBg    color.Color
	SelFg    color.Color
	SelBgHex string
	SelFgHex string
	Border   color.Color
	Red      color.Color
	Green    color.Color
	Yellow   color.Color
	Blue     color.Color
	Magenta  color.Color
	Cyan     color.Color

	HeaderBrand       lipgloss.Style
	HeaderBar         lipgloss.Style
	Pill              lipgloss.Style
	TabActive         lipgloss.Style
	TabInactive       lipgloss.Style
	Panel             lipgloss.Style
	PanelFocused      lipgloss.Style
	PanelTitle        lipgloss.Style
	SectionHeader     lipgloss.Style
	CollapsedHeader   lipgloss.Style
	RowSelected       lipgloss.Style
	RowMarked         lipgloss.Style
	RowSelectedMarked lipgloss.Style
	RowNormal         lipgloss.Style
	TableHeader       lipgloss.Style
	BadgeOK           lipgloss.Style
	BadgeWarn         lipgloss.Style
	BadgeErr          lipgloss.Style
	BadgeInfo         lipgloss.Style
	CLIBar            lipgloss.Style
	HelpBar           lipgloss.Style
	ModalBox          lipgloss.Style
	ModalTitle        lipgloss.Style
	ProgressBar       lipgloss.Style
	ProgressRest      lipgloss.Style

	KPIBox         lipgloss.Style
	KPILabel       lipgloss.Style
	KPIValue       lipgloss.Style
	Muted          lipgloss.Style
	AccentText     lipgloss.Style
	OKText         lipgloss.Style
	WarnText       lipgloss.Style
	ErrText        lipgloss.Style
	InfoText       lipgloss.Style
	DiffAdd        lipgloss.Style
	DiffDel        lipgloss.Style
	DiffHeader     lipgloss.Style
	ButtonActive   lipgloss.Style
	ButtonInactive lipgloss.Style
	ButtonDanger   lipgloss.Style
	BarColors      []color.Color
}

func parseHexRGB(hex string) (uint8, uint8, uint8, bool) {
	s := strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(s) != 6 {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return uint8(v >> 16), uint8((v >> 8) & 0xff), uint8(v & 0xff), true
}

func formatHexRGB(r, g, b uint8) string {
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

func srgbLinear(v uint8) float64 {
	c := float64(v) / 255.0
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func luminanceRGB(r, g, b uint8) float64 {
	return 0.2126*srgbLinear(r) + 0.7152*srgbLinear(g) + 0.0722*srgbLinear(b)
}

func contrastRatioRGB(r1, g1, b1, r2, g2, b2 uint8) float64 {
	l1 := luminanceRGB(r1, g1, b1)
	l2 := luminanceRGB(r2, g2, b2)
	if l1 < l2 {
		l1, l2 = l2, l1
	}
	return (l1 + 0.05) / (l2 + 0.05)
}

func ensureContrast(fgHex string, bgHexes []string, minRatio float64, isLight bool) string {
	fr, fg, fb, ok := parseHexRGB(fgHex)
	if !ok {
		return fgHex
	}
	type rgb struct{ r, g, b uint8 }
	bgs := make([]rgb, 0, len(bgHexes))
	for _, bh := range bgHexes {
		if r, g, b, bok := parseHexRGB(bh); bok {
			bgs = append(bgs, rgb{r, g, b})
		}
	}
	if len(bgs) == 0 {
		return fgHex
	}
	passes := func(r, g, b uint8) bool {
		for _, bg := range bgs {
			if contrastRatioRGB(r, g, b, bg.r, bg.g, bg.b) < minRatio {
				return false
			}
		}
		return true
	}
	if passes(fr, fg, fb) {
		return fgHex
	}
	for step := 1; step <= 40; step++ {
		t := float64(step) / 40.0
		var nr, ng, nb uint8
		if isLight {
			nr = uint8(math.Round(float64(fr) * (1.0 - t)))
			ng = uint8(math.Round(float64(fg) * (1.0 - t)))
			nb = uint8(math.Round(float64(fb) * (1.0 - t)))
		} else {
			nr = uint8(math.Round(float64(fr) + (255.0-float64(fr))*t))
			ng = uint8(math.Round(float64(fg) + (255.0-float64(fg))*t))
			nb = uint8(math.Round(float64(fb) + (255.0-float64(fb))*t))
		}
		if passes(nr, ng, nb) {
			return formatHexRGB(nr, ng, nb)
		}
	}
	if isLight {
		return "#000000"
	}
	return "#ffffff"
}

func NewStyles(pal config.Palette) Styles {
	roles := ui.DefaultRoles(pal)
	isLight := roles.IsLight

	themeID := pal.ID
	if themeID == "" {
		themeID = pal.Name
	}
	if themeID == "" {
		themeID = "gruvbox-dark"
	}

	none := lipgloss.NoColor{}
	if roles.NoColor {
		plain := lipgloss.NewStyle()
		return Styles{
			Palette:           pal,
			ThemeID:           themeID,
			IsLight:           isLight,
			Roles:             roles,
			Bg:                none,
			BgAlt:             none,
			Fg:                none,
			FgDim:             none,
			Accent:            none,
			SelBg:             none,
			SelFg:             none,
			SelBgHex:          "",
			SelFgHex:          "",
			Border:            none,
			Red:               none,
			Green:             none,
			Yellow:            none,
			Blue:              none,
			Magenta:           none,
			Cyan:              none,
			HeaderBrand:       plain.Padding(0, 1),
			HeaderBar:         plain,
			Pill:              plain.Padding(0, 1),
			TabActive:         plain.Padding(0, 1),
			TabInactive:       plain.Padding(0, 1),
			Panel:             plain.Border(lipgloss.RoundedBorder()).Padding(0, 1),
			PanelFocused:      plain.Border(lipgloss.RoundedBorder()).Padding(0, 1),
			PanelTitle:        plain,
			SectionHeader:     plain,
			CollapsedHeader:   plain,
			RowSelected:       plain,
			RowMarked:         plain,
			RowSelectedMarked: plain,
			RowNormal:         plain,
			TableHeader:       plain,
			BadgeOK:           plain.Padding(0, 1),
			BadgeWarn:         plain.Padding(0, 1),
			BadgeErr:          plain.Padding(0, 1),
			BadgeInfo:         plain.Padding(0, 1),
			CLIBar:            plain.Padding(0, 1),
			HelpBar:           plain.Padding(0, 1),
			ModalBox:          plain.Border(lipgloss.RoundedBorder()).Padding(1, 2),
			ModalTitle:        plain,
			ProgressBar:       plain,
			ProgressRest:      plain,
			KPIBox:            plain.Border(lipgloss.RoundedBorder()).Padding(0, 1),
			KPILabel:          plain,
			KPIValue:          plain,
			Muted:             plain,
			AccentText:        plain,
			OKText:            plain,
			WarnText:          plain,
			ErrText:           plain,
			InfoText:          plain,
			DiffAdd:           plain,
			DiffDel:           plain,
			DiffHeader:        plain,
			ButtonActive:      plain.Padding(0, 2),
			ButtonInactive:    plain.Padding(0, 2),
			ButtonDanger:      plain.Padding(0, 2),
			BarColors:         []color.Color{none, none, none, none, none, none},
		}
	}

	s := roles.Slots
	fallbackTokens := DefaultPalette().Tokens
	if isLight {
		fallbackTokens = defaultLightTokens()
	}
	selBgHex := ""
	selFgHex := ""
	if pal.Tokens != nil {
		selBgHex = strings.TrimSpace(pal.Tokens["selection_bg"])
		selFgHex = strings.TrimSpace(pal.Tokens["selection_fg"])
		if selFgHex == "" {
			selFgHex = strings.TrimSpace(pal.Tokens["fg"])
		}
	}
	if _, _, _, ok := parseHexRGB(selBgHex); !ok {
		selBgHex = fallbackTokens["selection_bg"]
	}
	if _, _, _, ok := parseHexRGB(selFgHex); !ok {
		selFgHex = fallbackTokens["selection_fg"]
		if selFgHex == "" {
			selFgHex = fallbackTokens["fg"]
		}
	}
	selFgHex = ensureContrast(selFgHex, []string{selBgHex}, 4.5, isLight)
	selBgCol := lipgloss.Color(selBgHex)
	selFgCol := lipgloss.Color(selFgHex)

	accentHex := fallbackTokens["accent"]
	yellowHex := fallbackTokens["color3"]
	if pal.Tokens != nil {
		if v := strings.TrimSpace(pal.Tokens["accent"]); v != "" {
			accentHex = v
		}
		if v := strings.TrimSpace(pal.Tokens["color3"]); v != "" {
			yellowHex = v
		}
	}
	tabActiveFg := s.Accent
	if _, _, _, ok := parseHexRGB(accentHex); ok {
		adj := ensureContrast(accentHex, []string{selBgHex}, 3.0, isLight)
		tabActiveFg = lipgloss.Color(adj)
	}
	rowSelMarkedFg := s.Yellow
	if _, _, _, ok := parseHexRGB(yellowHex); ok {
		adj := ensureContrast(yellowHex, []string{selBgHex}, 3.0, isLight)
		rowSelMarkedFg = lipgloss.Color(adj)
	}

	return Styles{
		Palette: pal,
		ThemeID: themeID,
		IsLight: isLight,
		Roles:   roles,

		Bg:       none,
		BgAlt:    none,
		Fg:       none,
		FgDim:    none,
		Accent:   s.Accent,
		SelBg:    selBgCol,
		SelFg:    selFgCol,
		SelBgHex: selBgHex,
		SelFgHex: selFgHex,
		Border:   none,
		Red:      s.Red,
		Green:    s.Green,
		Yellow:   s.Yellow,
		Blue:     s.Blue,
		Magenta:  s.Magenta,
		Cyan:     s.Cyan,

		HeaderBrand: lipgloss.NewStyle().
			Bold(true).
			Reverse(true).
			Padding(0, 1),
		HeaderBar: lipgloss.NewStyle(),
		Pill: lipgloss.NewStyle().
			Foreground(s.Cyan).
			Padding(0, 1),
		TabActive: lipgloss.NewStyle().
			Bold(true).
			Background(selBgCol).
			Foreground(tabActiveFg).
			Padding(0, 1),
		TabInactive: lipgloss.NewStyle().
			Faint(true).
			Padding(0, 1),
		Panel: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Padding(0, 1),
		PanelFocused: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(s.Accent).
			Padding(0, 1),
		PanelTitle: lipgloss.NewStyle().
			Bold(true).
			Foreground(s.Accent),
		SectionHeader: lipgloss.NewStyle().
			Bold(true).
			Foreground(s.Cyan),
		CollapsedHeader: lipgloss.NewStyle().
			Faint(true),
		RowSelected: lipgloss.NewStyle().
			Background(selBgCol).
			Foreground(selFgCol),
		RowMarked: lipgloss.NewStyle().
			Bold(true).
			Foreground(s.Yellow),
		RowSelectedMarked: lipgloss.NewStyle().
			Bold(true).
			Background(selBgCol).
			Foreground(rowSelMarkedFg),
		RowNormal: lipgloss.NewStyle(),
		TableHeader: lipgloss.NewStyle().
			Bold(true).
			Foreground(s.Cyan),
		BadgeOK: lipgloss.NewStyle().
			Bold(true).
			Foreground(s.Green).
			Padding(0, 1),
		BadgeWarn: lipgloss.NewStyle().
			Bold(true).
			Foreground(s.Yellow).
			Padding(0, 1),
		BadgeErr: lipgloss.NewStyle().
			Bold(true).
			Foreground(s.Red).
			Padding(0, 1),
		BadgeInfo: lipgloss.NewStyle().
			Bold(true).
			Foreground(s.Blue).
			Padding(0, 1),
		CLIBar: lipgloss.NewStyle().
			Foreground(s.Cyan).
			Padding(0, 1),
		HelpBar: lipgloss.NewStyle().
			Faint(true).
			Padding(0, 1),
		ModalBox: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(s.Accent).
			Padding(1, 2),
		ModalTitle: lipgloss.NewStyle().
			Bold(true).
			Foreground(s.Accent),
		ProgressBar:  roles.BarFill,
		ProgressRest: roles.BarTrack,

		KPIBox: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Padding(0, 1),
		KPILabel:       roles.Muted,
		KPIValue:       lipgloss.NewStyle().Bold(true),
		Muted:          roles.Muted,
		AccentText:     roles.Accent,
		OKText:         roles.OK,
		WarnText:       roles.Warn,
		ErrText:        roles.Err,
		InfoText:       roles.Info,
		DiffAdd:        lipgloss.NewStyle().Foreground(s.Green),
		DiffDel:        lipgloss.NewStyle().Foreground(s.Red),
		DiffHeader:     lipgloss.NewStyle().Bold(true).Foreground(s.Cyan),
		ButtonActive:   roles.ButtonActive,
		ButtonInactive: roles.ButtonInactive,
		ButtonDanger:   roles.ButtonDanger,
		BarColors:      []color.Color{s.Accent, s.Blue, s.Green, s.Magenta, s.Cyan, s.Yellow},
	}
}

func RenderSwatch(pal config.Palette, compact bool) string {
	isLight := strings.EqualFold(pal.Mode, "light") || strings.HasSuffix(strings.ToLower(pal.ID), "-light")
	fallback := DefaultPalette().Tokens
	if isLight {
		fallback = defaultLightTokens()
	}
	tok := func(k string) string {
		if pal.Tokens != nil {
			if v := strings.TrimSpace(pal.Tokens[k]); v != "" {
				return v
			}
		}
		return fallback[k]
	}

	glyph := "██"
	sep := " "
	if compact {
		glyph = "█"
		sep = ""
	}
	if ui.ColorDisabled() {
		return strings.Join([]string{glyph, glyph, glyph, glyph, glyph, glyph}, sep)
	}

	hexSlots := []string{
		tok("bg"),
		tok("accent"),
		tok("color2"),
		tok("color4"),
		tok("color3"),
		tok("color1"),
	}
	parts := make([]string, 0, len(hexSlots))
	for _, h := range hexSlots {
		parts = append(parts, lipgloss.NewStyle().Foreground(lipgloss.Color(h)).Render(glyph))
	}
	return strings.Join(parts, sep)
}
