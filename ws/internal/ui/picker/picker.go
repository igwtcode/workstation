package picker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/igwtcode/workstation/ws/internal/config"
	"github.com/igwtcode/workstation/ws/internal/fuzzy"
	"github.com/igwtcode/workstation/ws/internal/ui"
)

var (
	ErrCancelled = errors.New("picker cancelled")

	ErrEmpty = errors.New("no items to pick")
)

const (
	minVisibleRows = 6
	maxVisibleRows = 14
)

type ColumnSpec struct {
	Title  string
	Width  int
	Muted  bool
	Accent bool
}

type Item struct {
	Title    string
	Subtitle string
	Primary  string
	Columns  []string
	Value    string
	Filter   string
}

type Options struct {
	Title   string
	Prompt  string
	Query   string
	Items   []Item
	Columns []ColumnSpec
	Palette config.Palette
	Roles   *ui.Roles
	Input   io.Reader
	Output  io.Writer
	MaxRows int
	Width   int
	Height  int
}

type scoredItem struct {
	item         Item
	index        int
	score        int
	primaryScore int
	span         int
	primaryLen   int
}

func VisibleRows(termH int) int {
	if termH <= 0 {
		termH = 24
	}
	r := termH - 6
	if r < minVisibleRows {
		r = minVisibleRows
	}
	if r > maxVisibleRows {
		r = maxVisibleRows
	}
	return r
}

func HasInteractiveTTY() bool {
	return hasInteractiveTTY(os.Stdin, os.Stderr, canOpenDevTTY)
}

func hasInteractiveTTY(stdin, stderr *os.File, canDevTTY func() bool) bool {
	inTTY := isCharDevice(stdin)
	errTTY := isCharDevice(stderr)
	if inTTY && errTTY {
		return true
	}
	if (inTTY || errTTY) && canDevTTY != nil && canDevTTY() {
		return true
	}
	return false
}

func isCharDevice(f *os.File) bool {
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func canOpenDevTTY() bool {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

type Model struct {
	title        string
	allItems     []Item
	filtered     []scoredItem
	columns      []ColumnSpec
	hasColHeader bool
	allMaxTitle  int
	allMaxSub    int
	input        textinput.Model
	cursor       int
	offset       int
	width        int
	termHeight   int
	maxRows      int
	roles        ui.Roles
	selColors    ui.SelectionColors
	selected     *Item
	cancelled    bool
	quitting     bool
}

func NewModel(opts Options) Model {
	var roles ui.Roles
	if opts.Roles != nil {
		roles = *opts.Roles
	} else if opts.Output != nil {
		roles = ui.NewRolesForWriter(opts.Palette, opts.Output)
	} else {
		roles = ui.DefaultRoles(opts.Palette)
	}

	prompt := opts.Prompt
	if prompt == "" {
		prompt = "> "
	}

	ti := textinput.New()
	ti.Prompt = prompt
	ti.Placeholder = "type to filter..."
	ti.CharLimit = 256
	if opts.Query != "" {
		ti.SetValue(opts.Query)
	}
	applyInputStyles(&ti, roles)
	_ = ti.Focus()

	w := opts.Width
	if w <= 0 {
		w = 80
	}
	h := opts.Height
	if h <= 0 {
		h = 24
	}

	itemsCopy := make([]Item, len(opts.Items))
	copy(itemsCopy, opts.Items)

	var colsCopy []ColumnSpec
	hasColHeader := false
	if len(opts.Columns) > 0 {
		colsCopy = make([]ColumnSpec, len(opts.Columns))
		copy(colsCopy, opts.Columns)
		for _, c := range colsCopy {
			if strings.TrimSpace(c.Title) != "" {
				hasColHeader = true
				break
			}
		}
	}

	allMaxTitle, allMaxSub := 0, 0
	for _, it := range itemsCopy {
		if w := lipgloss.Width(it.Title); w > allMaxTitle {
			allMaxTitle = w
		}
		if w := lipgloss.Width(it.Subtitle); w > allMaxSub {
			allMaxSub = w
		}
	}

	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = "Select item"
	}

	selPal := opts.Palette
	if selPal.Mode == "" && selPal.ID == "" && roles.IsLight {
		selPal.Mode = "light"
	}

	m := Model{
		title:        title,
		allItems:     itemsCopy,
		columns:      colsCopy,
		hasColHeader: hasColHeader,
		allMaxTitle:  allMaxTitle,
		allMaxSub:    allMaxSub,
		input:        ti,
		width:        w,
		termHeight:   h,
		maxRows:      opts.MaxRows,
		roles:        roles,
		selColors:    ui.ResolveSelectionColors(selPal),
	}
	m.applyFilter(ti.Value())
	return m
}

func applyInputStyles(ti *textinput.Model, roles ui.Roles) {
	st := ti.Styles()
	if roles.NoColor {
		plain := lipgloss.NewStyle()
		st.Focused.Prompt = plain
		st.Focused.Text = plain
		st.Focused.Placeholder = plain
		st.Blurred.Prompt = plain
		st.Blurred.Text = plain
		st.Blurred.Placeholder = plain
		st.Cursor.Color = lipgloss.NoColor{}
		ti.SetVirtualCursor(false)
	} else {
		st.Focused.Prompt = roles.Accent
		st.Focused.Text = lipgloss.NewStyle()
		st.Focused.Placeholder = roles.Muted
		st.Blurred.Prompt = roles.Muted
		st.Blurred.Text = lipgloss.NewStyle()
		st.Blurred.Placeholder = roles.Muted
		ti.SetVirtualCursor(true)
	}
	ti.SetStyles(st)
}

func (m Model) Init() tea.Cmd {
	return textinput.Blink
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.termHeight = msg.Height
		}
		m.clampScroll()
		return m, nil

	case tea.QuitMsg, tea.InterruptMsg:
		m.quitting = true
		if m.selected == nil {
			m.cancelled = true
		}
		return m, tea.Quit

	case tea.MouseWheelMsg:
		switch msg.Mouse().Button {
		case tea.MouseWheelUp:
			if m.cursor > 0 {
				m.cursor--
				m.clampScroll()
			}
		case tea.MouseWheelDown:
			if m.cursor < len(m.filtered)-1 {
				m.cursor++
				m.clampScroll()
			}
		}
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "ctrl+c":
			m.cancelled = true
			m.quitting = true
			return m, tea.Quit
		case "enter":
			if len(m.filtered) > 0 && m.cursor >= 0 && m.cursor < len(m.filtered) {
				picked := m.filtered[m.cursor].item
				m.selected = &picked
				m.quitting = true
				return m, tea.Quit
			}
			return m, nil
		case "up", "ctrl+p", "ctrl+k":
			if m.cursor > 0 {
				m.cursor--
				m.clampScroll()
			}
			return m, nil
		case "down", "ctrl+n", "ctrl+j":
			if m.cursor < len(m.filtered)-1 {
				m.cursor++
				m.clampScroll()
			}
			return m, nil
		case "pgup", "ctrl+u":
			step := max(1, m.visibleCapacity()/2)
			m.cursor = max(0, m.cursor-step)
			m.clampScroll()
			return m, nil
		case "pgdown", "ctrl+d":
			if len(m.filtered) > 0 {
				step := max(1, m.visibleCapacity()/2)
				m.cursor = min(len(m.filtered)-1, m.cursor+step)
				m.clampScroll()
			}
			return m, nil
		case "home":
			m.cursor = 0
			m.clampScroll()
			return m, nil
		case "end":
			if len(m.filtered) > 0 {
				m.cursor = len(m.filtered) - 1
				m.clampScroll()
			}
			return m, nil
		}
	}

	prevQuery := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if curQuery := m.input.Value(); curQuery != prevQuery {
		m.applyFilter(curQuery)
	}
	return m, cmd
}

func (m Model) View() tea.View {
	if m.quitting || m.selected != nil || m.cancelled {
		return tea.NewView("")
	}
	v := tea.NewView(m.Render())
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m Model) Selected() (Item, bool) {
	if m.selected == nil || m.cancelled {
		return Item{}, false
	}
	return *m.selected, true
}

func (m Model) Cancelled() bool {
	return m.cancelled
}

func (m Model) Query() string {
	return m.input.Value()
}

func (m Model) Filtered() []Item {
	out := make([]Item, len(m.filtered))
	for i, s := range m.filtered {
		out[i] = s.item
	}
	return out
}

func (m Model) Cursor() int {
	return m.cursor
}

func (m Model) VisibleLimit() int {
	return m.visibleCapacity()
}

func (m Model) visibleCapacity() int {
	capRows := VisibleRows(m.termHeight)
	if m.termHeight > 0 && m.termHeight < 12 {
		capRows = max(1, m.termHeight-6)
	}
	if m.hasColHeader && capRows > 1 {
		capRows--
	}
	if m.maxRows > 0 && m.maxRows < capRows {
		capRows = m.maxRows
	}
	if capRows < 1 {
		capRows = 1
	}
	return capRows
}

func (m *Model) applyFilter(query string) {
	m.filtered = filterItems(m.allItems, query)
	m.cursor = 0
	m.offset = 0
	m.clampScroll()
}

func (m *Model) clampScroll() {
	n := len(m.filtered)
	if n == 0 {
		m.cursor = 0
		m.offset = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= n {
		m.cursor = n - 1
	}
	vis := m.visibleCapacity()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+vis {
		m.offset = m.cursor - vis + 1
	}
	if maxOffset := max(0, n-vis); m.offset > maxOffset {
		m.offset = maxOffset
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m Model) Render() string {
	boxW := m.width
	if boxW <= 0 {
		boxW = 80
	}
	if boxW < 16 {
		boxW = 16
	}
	innerW := max(10, boxW-4)

	countStr := fmt.Sprintf("%d/%d", len(m.filtered), len(m.allItems))
	topLine := m.renderTopBorder(boxW, countStr)
	sepLine := m.roles.Border.Render("├" + strings.Repeat("─", boxW-2) + "┤")
	botLine := m.roles.Border.Render("╰" + strings.Repeat("─", boxW-2) + "╯")

	inCopy := m.input
	inCopy.SetWidth(max(8, innerW-lipgloss.Width(inCopy.Prompt)-2))
	inputView := inCopy.View()
	if m.roles.NoColor {
		inputView = ui.StripANSI(inputView)
	}

	vis := m.visibleCapacity()
	rowsToShow := vis
	if len(m.filtered) == 0 {
		rowsToShow = 1
	} else if len(m.filtered) < rowsToShow {
		rowsToShow = len(m.filtered)
	}

	var lines []string
	lines = append(lines, topLine)
	lines = append(lines, m.wrapRow(inputView, innerW))
	lines = append(lines, sepLine)

	var colW []int
	if len(m.columns) > 0 {
		colW = m.resolveColumnWidths(innerW)
		if m.hasColHeader {
			lines = append(lines, m.wrapRow(m.renderColumnHeader(colW), innerW))
		}
	}

	tokens := activeHighlightTokens(m.input.Value())
	if len(m.filtered) == 0 {
		lines = append(lines, m.wrapRow(m.roles.Muted.Render("No matching items."), innerW))
	} else {
		end := min(len(m.filtered), m.offset+rowsToShow)
		if len(m.columns) > 0 {
			for i := m.offset; i < end; i++ {
				it := m.filtered[i].item
				lines = append(lines, m.wrapRow(m.renderColumnsRow(it, i == m.cursor, colW, innerW, tokens), innerW))
			}
		} else {
			titleColW, subColW := m.columnWidths(innerW)
			for i := m.offset; i < end; i++ {
				it := m.filtered[i].item
				lines = append(lines, m.wrapRow(m.renderRow(it, i == m.cursor, titleColW, subColW, innerW, tokens), innerW))
			}
		}
	}

	lines = append(lines, botLine)
	hint := "type to filter · ↑/↓ move · enter select · esc cancel"
	lines = append(lines, m.roles.Muted.Render(ui.TruncateDisplay(hint, boxW)))

	out := strings.Join(lines, "\n")
	if m.roles.NoColor {
		return ui.StripANSI(out)
	}
	return out
}

func (m Model) renderTopBorder(boxW int, countStr string) string {
	avail := max(4, boxW-2)
	countPlain := " " + countStr + " "
	maxTitle := avail - 2 - lipgloss.Width(countPlain) - 2
	if maxTitle < 4 {
		countPlain = ""
		maxTitle = max(1, avail-3)
	}
	titlePlain := " " + ui.TruncateDisplay(m.title, maxTitle) + " "
	fillW := avail - 1 - lipgloss.Width(titlePlain) - lipgloss.Width(countPlain)
	if countPlain != "" {
		fillW--
	}
	if fillW < 0 {
		fillW = 0
	}

	bdr := m.roles.Border
	if countPlain != "" {
		mid := bdr.Render("─") +
			m.roles.Header.Render(titlePlain) +
			bdr.Render(strings.Repeat("─", fillW)) +
			m.roles.Muted.Render(countPlain) +
			bdr.Render("─")
		return bdr.Render("╭") + mid + bdr.Render("╮")
	}
	mid := bdr.Render("─") + m.roles.Header.Render(titlePlain) + bdr.Render(strings.Repeat("─", fillW))
	return bdr.Render("╭") + mid + bdr.Render("╮")
}

func (m Model) wrapRow(content string, innerW int) string {
	if idx := strings.IndexByte(content, '\n'); idx >= 0 {
		content = content[:idx]
	}
	if ansi.StringWidth(content) > innerW {
		content = ansi.Truncate(content, innerW, "…")
	}
	if pad := innerW - ansi.StringWidth(content); pad > 0 {
		content += strings.Repeat(" ", pad)
	}
	vBar := m.roles.Border.Render("│")
	return vBar + " " + content + " " + vBar
}

func (m Model) columnWidths(innerW int) (titleW, subW int) {
	usable := max(6, innerW-2)
	maxTitle := m.allMaxTitle
	maxSub := m.allMaxSub
	if maxSub == 0 {
		return usable, 0
	}
	if maxTitle+2+maxSub <= usable {
		return maxTitle, usable - maxTitle - 2
	}
	subW = min(maxSub, max(10, usable*35/100))
	titleW = usable - subW - 2
	if titleW > maxTitle {
		titleW = maxTitle
		subW = min(maxSub, usable-titleW-2)
	}
	if titleW < 10 {
		titleW = max(6, usable/2)
		subW = max(0, usable-titleW-2)
	}
	return titleW, subW
}

func (m Model) resolveColumnWidths(innerW int) []int {
	n := len(m.columns)
	if n == 0 {
		return nil
	}
	usable := max(6, innerW-2)
	gap := 2
	totalGap := gap * (n - 1)
	avail := usable - totalGap
	if avail < n {
		out := make([]int, n)
		for i := range out {
			out[i] = max(1, usable/n)
		}
		return out
	}

	sumFixed := 0
	flexCount := 0
	for _, c := range m.columns {
		if c.Width > 0 {
			sumFixed += c.Width
		} else {
			flexCount++
		}
	}

	out := make([]int, n)
	minFlex := 18
	if usable < 60 {
		minFlex = max(8, usable/3)
	}

	if flexCount == 0 && sumFixed <= avail {
		for i, c := range m.columns {
			out[i] = c.Width
		}
		return out
	}

	if flexCount > 0 && sumFixed+flexCount*minFlex <= avail {
		rem := avail - sumFixed
		perFlex := rem / flexCount
		extra := rem % flexCount
		for i, c := range m.columns {
			if c.Width > 0 {
				out[i] = c.Width
				continue
			}
			w := perFlex
			if extra > 0 {
				w++
				extra--
			}
			out[i] = w
		}
		return out
	}

	fixedCount := n - flexCount
	flexTotal := min(avail/2, flexCount*minFlex)
	availFixed := max(fixedCount, avail-flexTotal)
	usedFixed := 0
	if sumFixed > 0 && fixedCount > 0 {
		for i, c := range m.columns {
			if c.Width <= 0 {
				continue
			}
			w := c.Width * availFixed / sumFixed
			minCol := min(6, c.Width)
			if w < minCol {
				w = minCol
			}
			if w > c.Width {
				w = c.Width
			}
			out[i] = w
			usedFixed += w
		}
		for usedFixed > availFixed {
			trimmed := false
			for i := range out {
				if m.columns[i].Width > 0 && out[i] > 1 {
					out[i]--
					usedFixed--
					trimmed = true
					if usedFixed <= availFixed {
						break
					}
				}
			}
			if !trimmed {
				break
			}
		}
	}

	if flexCount > 0 {
		rem := max(flexCount, avail-usedFixed)
		perFlex := rem / flexCount
		extra := rem % flexCount
		for i, c := range m.columns {
			if c.Width > 0 {
				continue
			}
			w := perFlex
			if extra > 0 {
				w++
				extra--
			}
			out[i] = w
		}
	}
	return out
}

func (m Model) renderColumnHeader(colW []int) string {
	parts := make([]string, len(m.columns))
	for i, spec := range m.columns {
		cell := ui.TruncateDisplay(spec.Title, colW[i])
		if i < len(m.columns)-1 {
			cell = padRight(cell, colW[i])
		}
		parts[i] = m.roles.Muted.Render(cell)
	}
	return "  " + strings.Join(parts, "  ")
}

func (m Model) selectionStyles() (base, lead, accent, muted, match lipgloss.Style) {
	if m.roles.NoColor {
		plain := lipgloss.NewStyle()
		return plain, plain, plain, plain, plain
	}
	base = lipgloss.NewStyle().Background(m.selColors.Bg).Foreground(m.selColors.Fg)
	lead = lipgloss.NewStyle().Bold(true).Background(m.selColors.Bg).Foreground(m.roles.Slots.Accent)
	accent = lipgloss.NewStyle().Bold(true).Background(m.selColors.Bg).Foreground(m.roles.Slots.Accent)
	muted = lipgloss.NewStyle().Background(m.selColors.Bg).Foreground(m.selColors.Fg).Faint(true)
	match = lipgloss.NewStyle().Bold(true).Underline(true).Background(m.selColors.Bg).Foreground(m.roles.Slots.Green)
	return base, lead, accent, muted, match
}

func (m Model) renderColumnsRow(it Item, selected bool, colW []int, innerW int, tokens []fuzzy.Token) string {
	lead := "  "
	if selected {
		lead = "> "
	}
	if !selected {
		parts := make([]string, len(m.columns))
		for i, spec := range m.columns {
			raw := ""
			if i < len(it.Columns) {
				raw = it.Columns[i]
			}
			cell := ui.TruncateDisplay(raw, colW[i])
			baseStyle := lipgloss.NewStyle()
			switch {
			case spec.Accent:
				baseStyle = m.roles.Accent
			case spec.Muted:
				baseStyle = m.roles.Muted
			}
			rendered := m.highlightCell(cell, baseStyle, m.roles.OK.Bold(true), tokens, schemeForText(raw))
			if i < len(m.columns)-1 {
				if pad := colW[i] - lipgloss.Width(cell); pad > 0 {
					rendered += strings.Repeat(" ", pad)
				}
			}
			parts[i] = rendered
		}
		return lead + strings.Join(parts, "  ")
	}

	selBase, selLead, selAccent, selMuted, selMatch := m.selectionStyles()
	var b strings.Builder
	b.WriteString(selLead.Render(lead))
	usedW := lipgloss.Width(lead)
	for i := range m.columns {
		if usedW >= innerW {
			break
		}
		if i > 0 {
			sepW := min(2, innerW-usedW)
			b.WriteString(selBase.Render(strings.Repeat(" ", sepW)))
			usedW += sepW
			if usedW >= innerW {
				break
			}
		}
		raw := ""
		if i < len(it.Columns) {
			raw = it.Columns[i]
		}
		maxW := min(colW[i], innerW-usedW)
		cell := ui.TruncateDisplay(raw, maxW)
		colStyle := selBase
		switch {
		case m.columns[i].Accent:
			colStyle = selAccent
		case m.columns[i].Muted:
			colStyle = selMuted
		}
		b.WriteString(m.highlightCell(cell, colStyle, selMatch, tokens, schemeForText(raw)))
		cellW := lipgloss.Width(cell)
		usedW += cellW
		if i < len(m.columns)-1 && cellW < maxW {
			pad := maxW - cellW
			b.WriteString(selBase.Render(strings.Repeat(" ", pad)))
			usedW += pad
		}
	}
	if usedW < innerW {
		b.WriteString(selBase.Render(strings.Repeat(" ", innerW-usedW)))
	}
	return b.String()
}

func (m Model) renderRow(it Item, selected bool, titleW, subW, innerW int, tokens []fuzzy.Token) string {
	lead := "  "
	if selected {
		lead = "> "
	}
	tStr := ui.TruncateDisplay(it.Title, titleW)
	if !selected {
		plainBase := lipgloss.NewStyle()
		renderedTitle := m.highlightCellRawPreserving(tStr, plainBase, m.roles.OK.Bold(true), tokens, schemeForText(it.Title))
		if subW <= 0 || it.Subtitle == "" {
			return lead + renderedTitle
		}
		sStr := ui.TruncateDisplay(it.Subtitle, subW)
		renderedSub := m.highlightCell(sStr, m.roles.Muted, m.roles.OK.Bold(true), tokens, schemeForText(it.Subtitle))
		pad := max(0, titleW-lipgloss.Width(tStr))
		return lead + renderedTitle + strings.Repeat(" ", pad) + "  " + renderedSub
	}

	selBase, selLead, _, selMuted, selMatch := m.selectionStyles()
	maxTitleW := min(titleW, max(0, innerW-lipgloss.Width(lead)))
	tStr = ui.TruncateDisplay(ui.StripANSI(it.Title), maxTitleW)
	var b strings.Builder
	b.WriteString(selLead.Render(lead))
	usedW := lipgloss.Width(lead)
	b.WriteString(m.highlightCell(tStr, selBase.Bold(true), selMatch, tokens, schemeForText(it.Title)))
	tVisW := lipgloss.Width(tStr)
	usedW += tVisW

	if subW > 0 && it.Subtitle != "" && usedW+2 < innerW {
		if pad := max(0, titleW-tVisW); pad > 0 {
			padClamped := min(pad, innerW-usedW)
			b.WriteString(selBase.Render(strings.Repeat(" ", padClamped)))
			usedW += padClamped
		}
		if usedW+2 <= innerW {
			b.WriteString(selBase.Render("  "))
			usedW += 2
		}
		if usedW < innerW {
			sStr := ui.TruncateDisplay(ui.StripANSI(it.Subtitle), min(subW, innerW-usedW))
			b.WriteString(m.highlightCell(sStr, selMuted, selMatch, tokens, schemeForText(it.Subtitle)))
			usedW += lipgloss.Width(sStr)
		}
	}
	if usedW < innerW {
		b.WriteString(selBase.Render(strings.Repeat(" ", innerW-usedW)))
	}
	return b.String()
}

func (m Model) highlightCellRawPreserving(text string, baseStyle, matchStyle lipgloss.Style, tokens []fuzzy.Token, scheme fuzzy.Scheme) string {
	if m.roles.NoColor || len(tokens) == 0 {
		return text
	}
	clean := ui.StripANSI(text)
	pos := fuzzy.MatchPositions(clean, tokens, scheme)
	if len(pos) == 0 {
		return text
	}
	return renderHighlightedRunes([]rune(clean), pos, baseStyle, matchStyle)
}

func (m Model) highlightCell(text string, baseStyle, matchStyle lipgloss.Style, tokens []fuzzy.Token, scheme fuzzy.Scheme) string {
	if text == "" {
		return ""
	}
	if m.roles.NoColor || len(tokens) == 0 {
		return baseStyle.Render(text)
	}
	clean := ui.StripANSI(text)
	pos := fuzzy.MatchPositions(clean, tokens, scheme)
	if len(pos) == 0 {
		return baseStyle.Render(text)
	}
	return renderHighlightedRunes([]rune(clean), pos, baseStyle, matchStyle)
}

func renderHighlightedRunes(runes []rune, positions []int, baseStyle, matchStyle lipgloss.Style) string {
	if len(runes) == 0 {
		return ""
	}
	isMatch := make([]bool, len(runes))
	for _, p := range positions {
		if p >= 0 && p < len(runes) {
			isMatch[p] = true
		}
	}
	var b strings.Builder
	start := 0
	for start < len(runes) {
		curMatch := isMatch[start]
		end := start + 1
		for end < len(runes) && isMatch[end] == curMatch {
			end++
		}
		seg := string(runes[start:end])
		if curMatch {
			b.WriteString(matchStyle.Render(seg))
		} else {
			b.WriteString(baseStyle.Render(seg))
		}
		start = end
	}
	return b.String()
}

func activeHighlightTokens(query string) []fuzzy.Token {
	tokens := fuzzy.ParseQuery(query)
	if len(tokens) == 0 {
		return nil
	}
	out := make([]fuzzy.Token, 0, len(tokens))
	for _, tok := range tokens {
		if !tok.Negate && tok.Pattern != "" {
			out = append(out, tok)
		}
	}
	return out
}

func schemeForText(s string) fuzzy.Scheme {
	if strings.Contains(s, "/") || strings.Contains(s, "\\") {
		return fuzzy.SchemePath
	}
	return fuzzy.SchemeDefault
}

func padRight(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

func Pick(ctx context.Context, opts Options) (Item, error) {
	if len(opts.Items) == 0 {
		return Item{}, ErrEmpty
	}
	if ctx == nil {
		ctx = context.Background()
	}

	in := opts.Input
	out := opts.Output
	var closeFiles []*os.File
	defer func() {
		for _, f := range closeFiles {
			_ = f.Close()
		}
	}()

	if in == nil || out == nil {
		ttyIn, ttyOut, err := tea.OpenTTY()
		if err == nil {
			if in == nil {
				in = ttyIn
				closeFiles = append(closeFiles, ttyIn)
			} else if ttyIn != nil {
				_ = ttyIn.Close()
			}
			if out == nil {
				out = ttyOut
				if ttyOut != nil && ttyOut != ttyIn {
					closeFiles = append(closeFiles, ttyOut)
				}
			} else if ttyOut != nil && ttyOut != ttyIn {
				_ = ttyOut.Close()
			}
		} else {
			if in == nil {
				in = os.Stdin
			}
			if out == nil {
				out = os.Stderr
			}
		}
	}

	if opts.Width <= 0 || opts.Height <= 0 {
		for _, cand := range []any{out, in} {
			f, ok := cand.(*os.File)
			if !ok || !term.IsTerminal(f.Fd()) {
				continue
			}
			w, h, gerr := term.GetSize(f.Fd())
			if gerr != nil {
				continue
			}
			if opts.Width <= 0 && w > 0 {
				opts.Width = w
			}
			if opts.Height <= 0 && h > 0 {
				opts.Height = h
			}
			if opts.Width > 0 && opts.Height > 0 {
				break
			}
		}
	}

	progOpts := []tea.ProgramOption{
		tea.WithContext(ctx),
		tea.WithInput(in),
		tea.WithOutput(out),
	}
	if opts.Width > 0 && opts.Height > 0 {
		progOpts = append(progOpts, tea.WithWindowSize(opts.Width, opts.Height))
	}

	p := tea.NewProgram(NewModel(opts), progOpts...)
	finalModel, err := p.Run()
	if err != nil {
		if errors.Is(err, tea.ErrInterrupted) || errors.Is(err, tea.ErrProgramKilled) || errors.Is(err, context.Canceled) {
			return Item{}, ErrCancelled
		}
		return Item{}, err
	}
	fm, ok := finalModel.(Model)
	if !ok {
		return Item{}, ErrCancelled
	}
	sel, ok := fm.Selected()
	if !ok {
		return Item{}, ErrCancelled
	}
	return sel, nil
}

func FilterItems(items []Item, query string) []Item {
	scored := filterItems(items, query)
	out := make([]Item, len(scored))
	for i, s := range scored {
		out[i] = s.item
	}
	return out
}

func filterItems(items []Item, query string) []scoredItem {
	tokens := fuzzy.ParseQuery(query)
	if len(tokens) == 0 {
		out := make([]scoredItem, len(items))
		for i, it := range items {
			out[i] = scoredItem{item: it, index: i}
		}
		return out
	}

	matches := make([]scoredItem, 0, len(items))
	for i, it := range items {
		fields, primaryLen := buildItemSearchFields(it)
		totalScore := 0
		totalPrimaryScore := 0
		totalSpan := 0
		ok := true

		for _, tok := range tokens {
			if tok.Negate {
				if anyFieldMatchesToken(fields, tok) {
					ok = false
					break
				}
				continue
			}
			s, pScore, span, matched := scoreItemToken(fields, tok)
			if !matched {
				ok = false
				break
			}
			totalScore += s
			totalPrimaryScore += pScore
			totalSpan += span
		}
		if ok {
			matches = append(matches, scoredItem{
				item:         it,
				index:        i,
				score:        totalScore,
				primaryScore: totalPrimaryScore,
				span:         totalSpan,
				primaryLen:   primaryLen,
			})
		}
	}

	sort.SliceStable(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if a.primaryScore != b.primaryScore {
			return a.primaryScore > b.primaryScore
		}
		if a.span != b.span {
			return a.span < b.span
		}
		if a.primaryLen != b.primaryLen {
			return a.primaryLen < b.primaryLen
		}
		if la, lb := len(a.item.Title), len(b.item.Title); la != lb {
			return la < lb
		}
		return a.index < b.index
	})
	return matches
}

type itemSearchField struct {
	text      string
	scheme    fuzzy.Scheme
	boost     int
	isPrimary bool
}

func buildItemSearchFields(it Item) ([]itemSearchField, int) {
	primary := strings.TrimSpace(it.Primary)
	if primary == "" {
		if strings.Contains(it.Title, "/") {
			trimmed := strings.TrimRight(it.Title, "/")
			if idx := strings.LastIndex(trimmed, "/"); idx >= 0 && idx+1 < len(trimmed) {
				primary = trimmed[idx+1:]
			}
		}
		if primary == "" {
			primary = it.Title
		}
	}
	primaryLen := len([]rune(primary))

	fields := make([]itemSearchField, 0, 5)
	if primary != "" {
		fields = append(fields, itemSearchField{
			text:      primary,
			scheme:    fuzzy.SchemeDefault,
			boost:     32,
			isPrimary: true,
		})
	}
	if it.Title != "" && it.Title != primary {
		fields = append(fields, itemSearchField{
			text:   it.Title,
			scheme: schemeForText(it.Title),
			boost:  0,
		})
	}
	if len(it.Columns) > 0 {
		joined := strings.Join(it.Columns, " ")
		if joined != "" && joined != primary && joined != it.Title {
			fields = append(fields, itemSearchField{
				text:   joined,
				scheme: schemeForText(joined),
				boost:  -24,
			})
		}
	}
	if it.Subtitle != "" && it.Subtitle != primary && it.Subtitle != it.Title {
		fields = append(fields, itemSearchField{
			text:   it.Subtitle,
			scheme: fuzzy.SchemeDefault,
			boost:  -24,
		})
	}
	if it.Filter != "" && it.Filter != primary && it.Filter != it.Title {
		fields = append(fields, itemSearchField{
			text:   it.Filter,
			scheme: schemeForText(it.Filter),
			boost:  -28,
		})
	}
	return fields, primaryLen
}

func anyFieldMatchesToken(fields []itemSearchField, tok fuzzy.Token) bool {
	for _, f := range fields {
		if res := fuzzy.MatchToken(f.text, tok, f.scheme, false); res.Matched {
			return true
		}
	}
	return false
}

func scoreItemToken(fields []itemSearchField, tok fuzzy.Token) (bestScore, primaryScore, bestSpan int, matched bool) {
	for _, f := range fields {
		res := fuzzy.MatchToken(f.text, tok, f.scheme, false)
		if !res.Matched {
			continue
		}
		adj := res.Score + f.boost
		span := res.End - res.Start + 1
		if f.isPrimary && adj > primaryScore {
			primaryScore = adj
		}
		if !matched || adj > bestScore || (adj == bestScore && (f.isPrimary || span < bestSpan)) {
			bestScore = adj
			bestSpan = span
			matched = true
		}
	}
	return bestScore, primaryScore, bestSpan, matched
}
