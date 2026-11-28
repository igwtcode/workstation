package ui

import (
	"fmt"
	"image/color"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/igwtcode/workstation/ws/internal/config"
)

func ColorDisabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb")
}

func IsTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok || f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func WriterColorEnabled(w io.Writer) bool {
	return !ColorDisabled() && IsTTY(w)
}

func StripANSI(s string) string {
	return ansi.Strip(s)
}

type SemanticSlots struct {
	RedIdx     int
	GreenIdx   int
	YellowIdx  int
	BlueIdx    int
	MagentaIdx int
	CyanIdx    int
	AccentIdx  int

	Red     color.Color
	Green   color.Color
	Yellow  color.Color
	Blue    color.Color
	Magenta color.Color
	Cyan    color.Color
	Accent  color.Color
}

type Roles struct {
	NoColor bool
	IsLight bool
	Slots   SemanticSlots

	Accent   lipgloss.Style
	Muted    lipgloss.Style
	OK       lipgloss.Style
	Warn     lipgloss.Style
	Err      lipgloss.Style
	Info     lipgloss.Style
	Header   lipgloss.Style
	Key      lipgloss.Style
	Border   lipgloss.Style
	Selected lipgloss.Style

	ButtonActive   lipgloss.Style
	ButtonInactive lipgloss.Style
	ButtonDanger   lipgloss.Style

	BarFill  lipgloss.Style
	BarTrack lipgloss.Style
}

func DefaultRoles(pal config.Palette) Roles {
	return NewRoles(pal, ColorDisabled())
}

func NewRolesForWriter(pal config.Palette, w io.Writer) Roles {
	return NewRoles(pal, !WriterColorEnabled(w))
}

func NewRoles(pal config.Palette, noColor bool) Roles {
	isLight := isLightPalette(pal)
	slots := ChooseSemanticSlots(pal)
	if noColor {
		plain := lipgloss.NewStyle()
		return Roles{
			NoColor:        true,
			IsLight:        isLight,
			Slots:          slots,
			Accent:         plain,
			Muted:          plain,
			OK:             plain,
			Warn:           plain,
			Err:            plain,
			Info:           plain,
			Header:         plain,
			Key:            plain,
			Border:         plain,
			Selected:       plain,
			ButtonActive:   plain,
			ButtonInactive: plain,
			ButtonDanger:   plain,
			BarFill:        plain,
			BarTrack:       plain,
		}
	}

	return Roles{
		NoColor:        false,
		IsLight:        isLight,
		Slots:          slots,
		Accent:         lipgloss.NewStyle().Bold(true).Foreground(slots.Accent),
		Muted:          lipgloss.NewStyle().Faint(true),
		OK:             lipgloss.NewStyle().Bold(true).Foreground(slots.Green),
		Warn:           lipgloss.NewStyle().Bold(true).Foreground(slots.Yellow),
		Err:            lipgloss.NewStyle().Bold(true).Foreground(slots.Red),
		Info:           lipgloss.NewStyle().Bold(true).Foreground(slots.Blue),
		Header:         lipgloss.NewStyle().Bold(true).Foreground(slots.Cyan),
		Key:            lipgloss.NewStyle().Foreground(slots.Cyan),
		Border:         lipgloss.NewStyle().Faint(true),
		Selected:       lipgloss.NewStyle().Bold(true).Reverse(true),
		ButtonActive:   lipgloss.NewStyle().Bold(true).Reverse(true).Padding(0, 2),
		ButtonInactive: lipgloss.NewStyle().Faint(true).Padding(0, 2),
		ButtonDanger:   lipgloss.NewStyle().Bold(true).Reverse(true).Foreground(slots.Red).Padding(0, 2),
		BarFill:        lipgloss.NewStyle().Foreground(slots.Accent),
		BarTrack:       lipgloss.NewStyle().Faint(true),
	}
}

func isLightPalette(pal config.Palette) bool {
	if strings.EqualFold(strings.TrimSpace(pal.Mode), "light") || strings.HasSuffix(strings.ToLower(pal.ID), "-light") {
		return true
	}
	if pal.Tokens != nil {
		if r, g, b, ok := ParseHexRGB(pal.Tokens["bg"]); ok {
			return luminanceRGB(r, g, b) > 0.35
		}
	}
	return false
}

func ChooseSemanticSlots(pal config.Palette) SemanticSlots {
	isLight := isLightPalette(pal)
	bgHex := ""
	accentHex := ""
	if pal.Tokens != nil {
		bgHex = strings.TrimSpace(pal.Tokens["bg"])
		accentHex = strings.TrimSpace(pal.Tokens["accent"])
	}

	pickPair := func(base int) int {
		bright := base + 8
		fallback := bright
		if isLight {
			fallback = base
		}
		if pal.Tokens == nil || bgHex == "" {
			return fallback
		}
		normHex := strings.TrimSpace(pal.Tokens[fmt.Sprintf("color%d", base)])
		brHex := strings.TrimSpace(pal.Tokens[fmt.Sprintf("color%d", bright)])
		if normHex == "" && brHex == "" {
			return fallback
		}
		if normHex == "" {
			return bright
		}
		if brHex == "" {
			return base
		}
		crNorm := ContrastRatioHex(normHex, bgHex)
		crBright := ContrastRatioHex(brHex, bgHex)
		if crBright > crNorm+0.15 {
			return bright
		}
		if crNorm > crBright+0.15 {
			return base
		}
		return fallback
	}

	redIdx := pickPair(1)
	greenIdx := pickPair(2)
	yellowIdx := pickPair(3)
	blueIdx := pickPair(4)
	magentaIdx := pickPair(5)
	cyanIdx := pickPair(6)

	accentIdx := pickAccentSlot(pal, bgHex, accentHex, []int{blueIdx, cyanIdx, yellowIdx, greenIdx, magentaIdx, redIdx})

	return SemanticSlots{
		RedIdx:     redIdx,
		GreenIdx:   greenIdx,
		YellowIdx:  yellowIdx,
		BlueIdx:    blueIdx,
		MagentaIdx: magentaIdx,
		CyanIdx:    cyanIdx,
		AccentIdx:  accentIdx,
		Red:        lipgloss.ANSIColor(redIdx),
		Green:      lipgloss.ANSIColor(greenIdx),
		Yellow:     lipgloss.ANSIColor(yellowIdx),
		Blue:       lipgloss.ANSIColor(blueIdx),
		Magenta:    lipgloss.ANSIColor(magentaIdx),
		Cyan:       lipgloss.ANSIColor(cyanIdx),
		Accent:     lipgloss.ANSIColor(accentIdx),
	}
}

func pickAccentSlot(pal config.Palette, bgHex, accentHex string, candidates []int) int {
	if len(candidates) == 0 {
		return 4
	}
	if pal.Tokens == nil || bgHex == "" || accentHex == "" {
		return candidates[0]
	}
	ar, ag, ab, ok := ParseHexRGB(accentHex)
	if !ok {
		return candidates[0]
	}

	bestIdx := candidates[0]
	bestScore := -1.0
	for _, slot := range candidates {
		hexVal := strings.TrimSpace(pal.Tokens[fmt.Sprintf("color%d", slot)])
		sr, sg, sb, sok := ParseHexRGB(hexVal)
		if !sok {
			continue
		}
		cr := ContrastRatioHex(hexVal, bgHex)
		if cr < 3.0 {
			continue
		}
		dr := float64(ar) - float64(sr)
		dg := float64(ag) - float64(sg)
		db := float64(ab) - float64(sb)
		dist := math.Sqrt(dr*dr + dg*dg + db*db)

		score := cr*40.0 - dist
		if score > bestScore {
			bestScore = score
			bestIdx = slot
		}
	}
	if bestScore < 0 {
		bestCr := 0.0
		for _, slot := range candidates {
			hexVal := strings.TrimSpace(pal.Tokens[fmt.Sprintf("color%d", slot)])
			if cr := ContrastRatioHex(hexVal, bgHex); cr > bestCr {
				bestCr = cr
				bestIdx = slot
			}
		}
	}
	return bestIdx
}

func ParseHexRGB(hex string) (uint8, uint8, uint8, bool) {
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

func ContrastRatioRGB(r1, g1, b1, r2, g2, b2 uint8) float64 {
	l1 := luminanceRGB(r1, g1, b1)
	l2 := luminanceRGB(r2, g2, b2)
	if l1 < l2 {
		l1, l2 = l2, l1
	}
	return (l1 + 0.05) / (l2 + 0.05)
}

func ContrastRatioHex(hexA, hexB string) float64 {
	r1, g1, b1, ok1 := ParseHexRGB(hexA)
	r2, g2, b2, ok2 := ParseHexRGB(hexB)
	if !ok1 || !ok2 {
		return 1.0
	}
	return ContrastRatioRGB(r1, g1, b1, r2, g2, b2)
}

type SelectionColors struct {
	BgHex string
	FgHex string
	Bg    color.Color
	Fg    color.Color
}

func ResolveSelectionColors(pal config.Palette) SelectionColors {
	isLight := isLightPalette(pal)
	fallbackBg := "#45475a"
	fallbackFg := "#cdd6f4"
	if isLight {
		fallbackBg = "#bdae93"
		fallbackFg = "#3c3836"
	}

	bgHex := ""
	fgHex := ""
	if pal.Tokens != nil {
		bgHex = strings.TrimSpace(pal.Tokens["selection_bg"])
		fgHex = strings.TrimSpace(pal.Tokens["selection_fg"])
		if fgHex == "" {
			fgHex = strings.TrimSpace(pal.Tokens["fg"])
		}
	}
	if _, _, _, ok := ParseHexRGB(bgHex); !ok {
		bgHex = fallbackBg
	}
	if _, _, _, ok := ParseHexRGB(fgHex); !ok {
		fgHex = fallbackFg
	}
	fgHex = ensureContrastHex(fgHex, bgHex, 4.5, isLight)
	return SelectionColors{
		BgHex: bgHex,
		FgHex: fgHex,
		Bg:    lipgloss.Color(bgHex),
		Fg:    lipgloss.Color(fgHex),
	}
}

func ensureContrastHex(fgHex, bgHex string, minRatio float64, isLight bool) string {
	fr, fg, fb, ok1 := ParseHexRGB(fgHex)
	br, bg, bb, ok2 := ParseHexRGB(bgHex)
	if !ok1 || !ok2 {
		return fgHex
	}
	if ContrastRatioRGB(fr, fg, fb, br, bg, bb) >= minRatio {
		return formatHexRGB(fr, fg, fb)
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
		if ContrastRatioRGB(nr, ng, nb, br, bg, bb) >= minRatio {
			return formatHexRGB(nr, ng, nb)
		}
	}
	if isLight {
		return "#000000"
	}
	return "#ffffff"
}

func formatHexRGB(r, g, b uint8) string {
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}
