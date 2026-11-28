package ui

import (
	"image/color"

	"charm.land/fang/v2"
	"charm.land/lipgloss/v2"
)

func FangScheme(c lipgloss.LightDarkFunc) fang.ColorScheme {
	if ColorDisabled() {
		return fang.ColorScheme{
			Base:           lipgloss.NoColor{},
			Title:          lipgloss.NoColor{},
			Description:    lipgloss.NoColor{},
			Codeblock:      lipgloss.NoColor{},
			Program:        lipgloss.NoColor{},
			DimmedArgument: lipgloss.NoColor{},
			Comment:        lipgloss.NoColor{},
			Flag:           lipgloss.NoColor{},
			FlagDefault:    lipgloss.NoColor{},
			Command:        lipgloss.NoColor{},
			QuotedString:   lipgloss.NoColor{},
			Argument:       lipgloss.NoColor{},
			Help:           lipgloss.NoColor{},
			Dash:           lipgloss.NoColor{},
			ErrorHeader:    [2]color.Color{lipgloss.NoColor{}, lipgloss.NoColor{}},
			ErrorDetails:   lipgloss.NoColor{},
		}
	}

	pick := func(lightSlot, darkSlot int) color.Color {
		if c == nil {
			return lipgloss.ANSIColor(darkSlot)
		}
		return c(lipgloss.ANSIColor(lightSlot), lipgloss.ANSIColor(darkSlot))
	}

	return fang.ColorScheme{
		Base:           lipgloss.NoColor{},
		Title:          pick(4, 12),
		Description:    lipgloss.NoColor{},
		Codeblock:      lipgloss.NoColor{},
		Program:        pick(4, 12),
		DimmedArgument: pick(6, 14),
		Comment:        pick(5, 13),
		Flag:           pick(2, 10),
		FlagDefault:    pick(3, 11),
		Command:        pick(6, 14),
		QuotedString:   pick(2, 10),
		Argument:       lipgloss.NoColor{},
		Help:           lipgloss.NoColor{},
		Dash:           lipgloss.NoColor{},
		ErrorHeader:    [2]color.Color{pick(1, 9), lipgloss.NoColor{}},
		ErrorDetails:   pick(1, 9),
	}
}
