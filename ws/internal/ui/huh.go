package ui

import (
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
)

type huhThemeAdapter func(bool) *huh.Styles

func (f huhThemeAdapter) Theme(isDark bool) *huh.Styles {
	return f(isDark)
}

func HuhTheme(roles Roles) huh.Theme {
	return huhThemeAdapter(func(isDark bool) *huh.Styles {
		t := huh.ThemeBase(isDark)

		t.Form.Base = lipgloss.NewStyle()
		t.Group.Base = lipgloss.NewStyle()

		if roles.NoColor || ColorDisabled() {
			plain := lipgloss.NewStyle()
			btn := lipgloss.NewStyle().Padding(0, 2).MarginRight(1)
			t.Group.Title = plain
			t.Group.Description = plain
			t.Focused.Base = lipgloss.NewStyle().PaddingLeft(1).BorderStyle(lipgloss.NormalBorder()).BorderLeft(true)
			t.Focused.Card = t.Focused.Base
			t.Focused.Title = plain
			t.Focused.NoteTitle = plain
			t.Focused.Description = plain
			t.Focused.ErrorIndicator = plain.SetString(" *")
			t.Focused.ErrorMessage = plain.SetString(" *")
			t.Focused.SelectSelector = plain.SetString("> ")
			t.Focused.Option = plain
			t.Focused.NextIndicator = plain.MarginLeft(1).SetString("→")
			t.Focused.PrevIndicator = plain.MarginRight(1).SetString("←")
			t.Focused.Directory = plain
			t.Focused.File = plain
			t.Focused.MultiSelectSelector = plain.SetString("> ")
			t.Focused.SelectedOption = plain
			t.Focused.SelectedPrefix = plain.SetString("[*] ")
			t.Focused.UnselectedOption = plain
			t.Focused.UnselectedPrefix = plain.SetString("[ ] ")
			t.Focused.FocusedButton = btn.SetString("")
			t.Focused.BlurredButton = btn
			t.Focused.Next = btn
			t.Focused.TextInput.Cursor = plain
			t.Focused.TextInput.CursorText = plain
			t.Focused.TextInput.Placeholder = plain
			t.Focused.TextInput.Prompt = plain
			t.Focused.TextInput.Text = plain

			t.Blurred = t.Focused
			t.Blurred.Base = t.Blurred.Base.BorderStyle(lipgloss.HiddenBorder())
			t.Blurred.Card = t.Blurred.Base
			t.Blurred.MultiSelectSelector = plain.SetString("  ")
			t.Blurred.NextIndicator = plain
			t.Blurred.PrevIndicator = plain
			t.Help.ShortKey = plain
			t.Help.ShortDesc = plain
			t.Help.ShortSeparator = plain
			t.Help.FullKey = plain
			t.Help.FullDesc = plain
			t.Help.FullSeparator = plain
			t.Help.Ellipsis = plain
			return t
		}

		accent := roles.Slots.Accent
		cyan := roles.Slots.Cyan
		green := roles.Slots.Green
		red := roles.Slots.Red
		blue := roles.Slots.Blue

		t.Group.Title = lipgloss.NewStyle().Bold(true).Foreground(accent)
		t.Group.Description = lipgloss.NewStyle().Faint(true)

		t.Focused.Base = lipgloss.NewStyle().
			PaddingLeft(1).
			BorderStyle(lipgloss.NormalBorder()).
			BorderLeft(true).
			BorderForeground(accent)
		t.Focused.Card = t.Focused.Base
		t.Focused.Title = lipgloss.NewStyle().Bold(true).Foreground(accent)
		t.Focused.NoteTitle = lipgloss.NewStyle().Bold(true).Foreground(cyan)
		t.Focused.Description = lipgloss.NewStyle().Faint(true)
		t.Focused.ErrorIndicator = lipgloss.NewStyle().Bold(true).Foreground(red).SetString(" *")
		t.Focused.ErrorMessage = lipgloss.NewStyle().Foreground(red).SetString(" *")
		t.Focused.SelectSelector = lipgloss.NewStyle().Bold(true).Foreground(accent).SetString("> ")
		t.Focused.Option = lipgloss.NewStyle()
		t.Focused.NextIndicator = lipgloss.NewStyle().Foreground(cyan).MarginLeft(1).SetString("→")
		t.Focused.PrevIndicator = lipgloss.NewStyle().Foreground(cyan).MarginRight(1).SetString("←")
		t.Focused.Directory = lipgloss.NewStyle().Bold(true).Foreground(blue)
		t.Focused.File = lipgloss.NewStyle()

		t.Focused.MultiSelectSelector = lipgloss.NewStyle().Bold(true).Foreground(accent).SetString("> ")
		t.Focused.SelectedOption = lipgloss.NewStyle().Bold(true).Foreground(green)
		t.Focused.SelectedPrefix = lipgloss.NewStyle().Bold(true).Foreground(green).SetString("[•] ")
		t.Focused.UnselectedOption = lipgloss.NewStyle()
		t.Focused.UnselectedPrefix = lipgloss.NewStyle().Faint(true).SetString("[ ] ")

		t.Focused.FocusedButton = lipgloss.NewStyle().
			Padding(0, 2).
			MarginRight(1).
			Bold(true).
			Reverse(true)
		t.Focused.BlurredButton = lipgloss.NewStyle().
			Padding(0, 2).
			MarginRight(1).
			Faint(true)
		t.Focused.Next = t.Focused.FocusedButton

		t.Focused.TextInput.Cursor = lipgloss.NewStyle().Reverse(true)
		t.Focused.TextInput.CursorText = lipgloss.NewStyle()
		t.Focused.TextInput.Placeholder = lipgloss.NewStyle().Faint(true)
		t.Focused.TextInput.Prompt = lipgloss.NewStyle().Bold(true).Foreground(cyan)
		t.Focused.TextInput.Text = lipgloss.NewStyle()

		t.Blurred = t.Focused
		t.Blurred.Base = t.Blurred.Base.BorderStyle(lipgloss.HiddenBorder())
		t.Blurred.Card = t.Blurred.Base
		t.Blurred.Title = lipgloss.NewStyle().Faint(true)
		t.Blurred.NoteTitle = lipgloss.NewStyle().Faint(true)
		t.Blurred.MultiSelectSelector = lipgloss.NewStyle().SetString("  ")
		t.Blurred.NextIndicator = lipgloss.NewStyle()
		t.Blurred.PrevIndicator = lipgloss.NewStyle()
		t.Blurred.TextInput.Prompt = lipgloss.NewStyle().Faint(true)
		t.Blurred.TextInput.Text = lipgloss.NewStyle().Faint(true)

		t.Help.ShortKey = lipgloss.NewStyle().Bold(true).Foreground(cyan)
		t.Help.ShortDesc = lipgloss.NewStyle().Faint(true)
		t.Help.ShortSeparator = lipgloss.NewStyle().Faint(true)
		t.Help.FullKey = lipgloss.NewStyle().Bold(true).Foreground(cyan)
		t.Help.FullDesc = lipgloss.NewStyle().Faint(true)
		t.Help.FullSeparator = lipgloss.NewStyle().Faint(true)
		t.Help.Ellipsis = lipgloss.NewStyle().Faint(true)

		return t
	})
}
