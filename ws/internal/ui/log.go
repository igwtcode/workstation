package ui

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"

	"charm.land/lipgloss/v2"
)

func FormatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	mins := int(d / time.Minute)
	secs := int((d % time.Minute).Round(time.Second) / time.Second)
	if secs == 60 {
		mins++
		secs = 0
	}
	return fmt.Sprintf("%dm%ds", mins, secs)
}

func FormatSpinnerElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	mins := int(d / time.Minute)
	secs := int((d % time.Minute).Round(time.Second) / time.Second)
	if secs == 60 {
		mins++
		secs = 0
	}
	return fmt.Sprintf("%dm%02ds", mins, secs)
}

type Logger struct {
	w     io.Writer
	roles Roles
	wrote bool
}

func NewLogger(w io.Writer, roles Roles) *Logger {
	if w == nil {
		w = io.Discard
	}
	return &Logger{w: w, roles: roles}
}

func (l *Logger) BlankLine() {
	if l.wrote {
		_, _ = io.WriteString(l.w, "\n")
	}
}

func (l *Logger) Info(msg string, kv ...any) {
	l.levelLine("INFO", l.roles.Info, msg, kv...)
}

func (l *Logger) Done(msg string, kv ...any) {
	l.levelLine("DONE", l.roles.OK, msg, kv...)
}

func (l *Logger) Warn(msg string, kv ...any) {
	l.levelLine("WARN", l.roles.Warn, msg, kv...)
}

func (l *Logger) Error(msg string, kv ...any) {
	l.levelLine("ERRO", l.roles.Err, msg, kv...)
}

func (l *Logger) Skip(msg string, kv ...any) {
	l.levelLine("SKIP", l.roles.Muted, msg, kv...)
}

func (l *Logger) Plan(msg string, kv ...any) {
	l.levelLine("PLAN", l.roles.Accent, msg, kv...)
}

func (l *Logger) levelLine(badge string, badgeStyle lipgloss.Style, msg string, kv ...any) {
	_, _ = fmt.Fprintf(l.w, "%s  %s%s\n", badgeStyle.Render(badge), msg, formatKV(l.roles, kv...))
	l.wrote = true
}

func (l *Logger) Section(title string) {
	if strings.TrimSpace(title) == "" {
		return
	}
	if l.wrote {
		_, _ = io.WriteString(l.w, "\n")
	}
	_, _ = fmt.Fprintln(l.w, l.roles.Header.Render(title))
	l.wrote = true
}

func (l *Logger) StepStart(summary string) {
	_, _ = fmt.Fprintf(l.w, "  %s %s\n", l.roles.Accent.Render("›"), l.roles.Muted.Render(summary))
	l.wrote = true
}

func (l *Logger) StepDone(summary string, elapsed time.Duration, detail string) {
	line := "  " + l.roles.OK.Render("✓") + " " + summary
	if elapsed > 0 {
		line += "  " + l.roles.Muted.Render(FormatDuration(elapsed))
	}
	if detail != "" {
		line += l.roles.Muted.Render(" · " + detail)
	}
	_, _ = fmt.Fprintln(l.w, line)
	l.wrote = true
}

func (l *Logger) StepSkip(summary string, detail string) {
	line := "  " + l.roles.Muted.Render("–") + " " + l.roles.Muted.Render(summary)
	if detail != "" {
		line += "  " + l.roles.Muted.Render(detail)
	}
	_, _ = fmt.Fprintln(l.w, line)
	l.wrote = true
}

func (l *Logger) StepWarn(summary string, elapsed time.Duration, detail string) {
	line := "  " + l.roles.Warn.Render("!") + " " + summary
	if elapsed > 0 {
		line += "  " + l.roles.Muted.Render(FormatDuration(elapsed))
	}
	if detail != "" {
		line += ": " + l.roles.Warn.Render(detail)
	}
	_, _ = fmt.Fprintln(l.w, line)
	l.wrote = true
}

func (l *Logger) StepFail(summary string, elapsed time.Duration, detail string) {
	line := "  " + l.roles.Err.Render("✗") + " " + summary
	if elapsed > 0 {
		line += "  " + l.roles.Muted.Render(FormatDuration(elapsed))
	}
	if detail != "" {
		line += ": " + l.roles.Err.Render(detail)
	}
	_, _ = fmt.Fprintln(l.w, line)
	l.wrote = true
}

func (l *Logger) PlanItem(kind, summary string) {
	_, _ = fmt.Fprintf(l.w, "  %s %s %s\n", l.roles.Muted.Render("·"), l.roles.Key.Render("["+kind+"]"), summary)
	l.wrote = true
}

func (l *Logger) ChangeItem(summary string) {
	_, _ = fmt.Fprintf(l.w, "  %s %s\n", l.roles.OK.Render("+"), summary)
	l.wrote = true
}

func (l *Logger) ChildLine(line string) {
	_, _ = fmt.Fprintf(l.w, "    %s\n", l.roles.Muted.Render("│ "+line))
	l.wrote = true
}

func (l *Logger) KV(label string, width int, value string) {
	lbl := label + ":"
	if pad := width - len(lbl); pad > 0 {
		lbl += strings.Repeat(" ", pad)
	}
	_, _ = fmt.Fprintf(l.w, "%s %s\n", l.roles.Key.Render(lbl), value)
	l.wrote = true
}

func formatKV(roles Roles, kv ...any) string {
	if len(kv) < 2 {
		return ""
	}
	var b strings.Builder
	for i := 0; i+1 < len(kv); i += 2 {
		k := strings.TrimSpace(fmt.Sprint(kv[i]))
		if k == "" {
			continue
		}
		v := formatKVValue(kv[i+1])
		b.WriteString("  ")
		b.WriteString(roles.Muted.Render(k + "="))
		b.WriteString(v)
	}
	return b.String()
}

func formatKVValue(v any) string {
	s := fmt.Sprint(v)
	if s == "" || strings.IndexFunc(s, unicode.IsSpace) >= 0 || strings.ContainsRune(s, '"') {
		return strconv.Quote(s)
	}
	return s
}
