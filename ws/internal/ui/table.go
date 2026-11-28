package ui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type TableColumn struct {
	Title      string
	MinWidth   int
	MaxWidth   int
	RightAlign bool
}

func TruncateDisplay(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", " ")
	if ansi.StringWidth(s) <= maxW {
		return s
	}
	if maxW == 1 {
		return "…"
	}
	return ansi.Truncate(s, maxW, "…")
}

func RenderTSV(w io.Writer, rows [][]string) error {
	for _, row := range rows {
		clean := make([]string, len(row))
		for i, cell := range row {
			c := StripANSI(cell)
			c = strings.ReplaceAll(c, "\t", " ")
			c = strings.ReplaceAll(c, "\r", "")
			c = strings.ReplaceAll(c, "\n", " ")
			clean[i] = c
		}
		if _, err := fmt.Fprintln(w, strings.Join(clean, "\t")); err != nil {
			return err
		}
	}
	return nil
}

func RenderTable(w io.Writer, cols []TableColumn, rows [][]string, r Roles) error {
	if len(cols) == 0 {
		return nil
	}
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = max(c.MinWidth, ansi.StringWidth(StripANSI(c.Title)))
	}
	for _, row := range rows {
		for i := 0; i < len(cols) && i < len(row); i++ {
			cw := ansi.StringWidth(StripANSI(row[i]))
			if cw > widths[i] {
				widths[i] = cw
			}
		}
	}
	for i, c := range cols {
		if c.MaxWidth > 0 && widths[i] > c.MaxWidth {
			widths[i] = c.MaxWidth
		}
	}

	formatCell := func(raw string, i int, isHeader bool) string {
		plain := StripANSI(raw)
		plain = TruncateDisplay(plain, widths[i])
		pad := widths[i] - ansi.StringWidth(plain)
		if pad < 0 {
			pad = 0
		}
		if cols[i].RightAlign {
			plain = strings.Repeat(" ", pad) + plain
		} else if i < len(cols)-1 {
			plain += strings.Repeat(" ", pad)
		}
		if r.NoColor {
			return plain
		}
		if isHeader {
			return r.Header.Render(plain)
		}
		return plain
	}

	hdrParts := make([]string, len(cols))
	for i, c := range cols {
		hdrParts[i] = formatCell(c.Title, i, true)
	}
	if _, err := fmt.Fprintln(w, strings.Join(hdrParts, "  ")); err != nil {
		return err
	}

	for _, row := range rows {
		parts := make([]string, len(cols))
		for i := range cols {
			val := ""
			if i < len(row) {
				val = row[i]
			}
			if !r.NoColor && strings.Contains(val, "\x1b") {
				styled := TruncateDisplay(val, widths[i])
				pad := widths[i] - ansi.StringWidth(styled)
				if cols[i].RightAlign {
					parts[i] = strings.Repeat(" ", max(0, pad)) + styled
				} else if i < len(cols)-1 {
					parts[i] = styled + strings.Repeat(" ", max(0, pad))
				} else {
					parts[i] = styled
				}
				continue
			}
			parts[i] = formatCell(val, i, false)
		}
		if _, err := fmt.Fprintln(w, strings.Join(parts, "  ")); err != nil {
			return err
		}
	}
	return nil
}
