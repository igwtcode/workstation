package ui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/term"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type ProgressOptions struct {
	ForceTTY     bool
	Width        int
	TickInterval time.Duration
}

type activeTask struct {
	id            string
	summary       string
	startedAt     time.Time
	headerPrinted bool
	needsTTY      bool
}

type Progress struct {
	w      io.Writer
	logger *Logger
	roles  Roles
	opts   ProgressOptions
	isTTY  bool

	mu             sync.Mutex
	tasks          []*activeTask
	frameIdx       int
	spinnerVisible bool
	paused         bool
	closed         bool
	childBuf       bytes.Buffer

	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once
}

func NewProgress(w io.Writer, roles Roles) *Progress {
	return NewProgressWithOptions(w, roles, ProgressOptions{})
}

func NewProgressWithOptions(w io.Writer, roles Roles, opts ProgressOptions) *Progress {
	if w == nil {
		w = io.Discard
	}
	if opts.TickInterval <= 0 {
		opts.TickInterval = 100 * time.Millisecond
	}
	isTTY := opts.ForceTTY || (IsTTY(w) && !strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb"))
	p := &Progress{
		w:      w,
		logger: NewLogger(w, roles),
		roles:  roles,
		opts:   opts,
		isTTY:  isTTY,
	}
	if isTTY {
		p.stopCh = make(chan struct{})
		p.doneCh = make(chan struct{})
		go p.runTicker()
	}
	return p
}

func (p *Progress) runTicker() {
	ticker := time.NewTicker(p.opts.TickInterval)
	defer ticker.Stop()
	defer close(p.doneCh)
	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.mu.Lock()
			if !p.closed && p.isTTY && !p.paused && len(p.tasks) > 0 {
				p.frameIdx = (p.frameIdx + 1) % len(spinnerFrames)
				p.renderSpinnerLocked()
			}
			p.mu.Unlock()
		}
	}
}

func (p *Progress) Section(title string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.flushChildLocked()
	p.clearSpinnerLocked()
	p.logger.Section(title)
	if !p.closed && p.isTTY && !p.paused && len(p.tasks) > 0 {
		p.renderSpinnerLocked()
	}
}

func (p *Progress) Start(id, summary string) {
	p.StartWithTTY(id, summary, false)
}

func (p *Progress) StartWithTTY(id, summary string, needsTTY bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.flushChildLocked()
	p.clearSpinnerLocked()

	t := &activeTask{
		id:        id,
		summary:   summary,
		startedAt: time.Now(),
		needsTTY:  needsTTY,
	}
	if needsTTY {
		t.headerPrinted = true
		p.paused = true
		p.logger.StepStart(summary)
	} else if !p.isTTY {
		t.headerPrinted = true
		p.logger.StepStart(summary)
	}
	p.tasks = append(p.tasks, t)
	if !p.closed && p.isTTY && !p.paused && len(p.tasks) > 0 {
		p.renderSpinnerLocked()
	}
}

func (p *Progress) Done(id, summary string, elapsed time.Duration, detail string) {
	p.finishTask(id, summary, func() {
		p.logger.StepDone(summary, elapsed, detail)
	})
}

func (p *Progress) Skip(id, summary string, detail string) {
	p.finishTask(id, summary, func() {
		p.logger.StepSkip(summary, detail)
	})
}

func (p *Progress) Warn(id, summary string, elapsed time.Duration, detail string) {
	p.finishTask(id, summary, func() {
		p.logger.StepWarn(summary, elapsed, detail)
	})
}

func (p *Progress) Fail(id, summary string, elapsed time.Duration, detail string) {
	p.finishTask(id, summary, func() {
		p.logger.StepFail(summary, elapsed, detail)
	})
}

func (p *Progress) finishTask(id, summary string, printFn func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.flushChildLocked()
	p.clearSpinnerLocked()
	p.removeTaskLocked(id, summary)
	p.recomputePausedLocked()
	printFn()
	if !p.closed && p.isTTY && !p.paused && len(p.tasks) > 0 {
		p.renderSpinnerLocked()
	}
}

func (p *Progress) Info(msg string, kv ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.flushChildLocked()
	p.clearSpinnerLocked()
	p.logger.Info(msg, kv...)
	if !p.closed && p.isTTY && !p.paused && len(p.tasks) > 0 {
		p.renderSpinnerLocked()
	}
}

func (p *Progress) WarnLog(msg string, kv ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.flushChildLocked()
	p.clearSpinnerLocked()
	p.logger.Warn(msg, kv...)
	if !p.closed && p.isTTY && !p.paused && len(p.tasks) > 0 {
		p.renderSpinnerLocked()
	}
}

func (p *Progress) DoneLog(msg string, kv ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.flushChildLocked()
	p.clearSpinnerLocked()
	p.logger.Done(msg, kv...)
	if !p.closed && p.isTTY && !p.paused && len(p.tasks) > 0 {
		p.renderSpinnerLocked()
	}
}

func (p *Progress) ErrorLog(msg string, kv ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.flushChildLocked()
	p.clearSpinnerLocked()
	p.logger.Error(msg, kv...)
	if !p.closed && p.isTTY && !p.paused && len(p.tasks) > 0 {
		p.renderSpinnerLocked()
	}
}

func (p *Progress) ChildWriter() io.Writer {
	return &progressChildWriter{p: p}
}

func (p *Progress) Close() {
	p.stopOnce.Do(func() {
		if p.stopCh != nil {
			close(p.stopCh)
		}
		if p.doneCh != nil {
			<-p.doneCh
		}
	})
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.flushChildLocked()
	p.clearSpinnerLocked()
}

func Spin(w io.Writer, roles Roles, title string, fn func() error) error {
	return SpinWithOptions(w, roles, title, ProgressOptions{}, fn)
}

func SpinWithOptions(w io.Writer, roles Roles, title string, opts ProgressOptions, fn func() error) error {
	isTTY := opts.ForceTTY || (IsTTY(w) && !strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb"))
	if !isTTY {
		return fn()
	}
	p := NewProgressWithOptions(w, roles, opts)
	p.Start("spin", title)
	err := fn()
	p.Close()
	return err
}

type progressChildWriter struct {
	p *Progress
}

func (cw *progressChildWriter) Write(b []byte) (int, error) {
	cw.p.mu.Lock()
	defer cw.p.mu.Unlock()

	cw.p.childBuf.Write(b)
	for {
		data := cw.p.childBuf.Bytes()
		idx := bytes.IndexByte(data, '\n')
		if idx < 0 {
			break
		}
		rawLine := string(data[:idx])
		cw.p.childBuf.Next(idx + 1)
		cw.p.emitRawChildLineLocked(rawLine)
	}
	if !cw.p.closed && cw.p.isTTY && !cw.p.paused && len(cw.p.tasks) > 0 {
		cw.p.renderSpinnerLocked()
	}
	return len(b), nil
}

func (p *Progress) flushChildLocked() {
	if p.childBuf.Len() == 0 {
		return
	}
	rem := p.childBuf.String()
	p.childBuf.Reset()
	p.emitRawChildLineLocked(rem)
}

func (p *Progress) emitRawChildLineLocked(raw string) {
	line := normalizeChildLine(raw, p.roles.NoColor)
	if line == "" {
		return
	}
	p.clearSpinnerLocked()
	if len(p.tasks) > 0 {
		top := p.tasks[len(p.tasks)-1]
		if !top.headerPrinted {
			top.headerPrinted = true
			p.logger.StepStart(top.summary)
		}
	}
	p.logger.ChildLine(line)
}

func normalizeChildLine(raw string, noColor bool) string {

	if strings.ContainsRune(raw, '\r') {
		parts := strings.Split(raw, "\r")
		chosen := ""
		for i := len(parts) - 1; i >= 0; i-- {
			cand := strings.TrimRight(parts[i], " \t\r")
			if strings.TrimSpace(StripANSI(cand)) != "" {
				chosen = cand
				break
			}
		}
		raw = chosen
	}
	if noColor {
		raw = StripANSI(raw)
	}
	raw = strings.TrimRight(raw, " \t\r")
	if strings.TrimSpace(StripANSI(raw)) == "" {
		return ""
	}
	return raw
}

func (p *Progress) removeTaskLocked(id, summary string) {
	if len(p.tasks) == 0 {
		return
	}
	if id != "" {
		for i, t := range p.tasks {
			if t.id == id {
				p.tasks = append(p.tasks[:i], p.tasks[i+1:]...)
				return
			}
		}
	}
	if summary != "" {
		for i, t := range p.tasks {
			if t.summary == summary {
				p.tasks = append(p.tasks[:i], p.tasks[i+1:]...)
				return
			}
		}
	}
}

func (p *Progress) recomputePausedLocked() {
	for _, t := range p.tasks {
		if t.needsTTY {
			p.paused = true
			return
		}
	}
	p.paused = false
}

func (p *Progress) clearSpinnerLocked() {
	if !p.spinnerVisible {
		return
	}
	_, _ = io.WriteString(p.w, "\r\x1b[2K")
	p.spinnerVisible = false
}

func (p *Progress) renderSpinnerLocked() {
	if len(p.tasks) == 0 {
		return
	}
	now := time.Now()
	maxElapsed := time.Duration(0)
	for _, t := range p.tasks {
		if d := now.Sub(t.startedAt); d > maxElapsed {
			maxElapsed = d
		}
	}
	var label string
	switch len(p.tasks) {
	case 1:
		label = p.tasks[0].summary
	case 2:
		label = p.tasks[0].summary + ", " + p.tasks[1].summary
	default:
		label = fmt.Sprintf("Running %d tasks: %s, %s (+%d more)", len(p.tasks), p.tasks[0].summary, p.tasks[1].summary, len(p.tasks)-2)
	}

	frame := spinnerFrames[p.frameIdx%len(spinnerFrames)]
	styledFrame := p.roles.Accent.Render(frame)
	elapsedText := p.roles.Muted.Render(FormatSpinnerElapsed(maxElapsed))
	line := "  " + styledFrame + " " + label + "  " + elapsedText
	if limit := max(12, p.termWidthLocked()-1); limit > 0 {
		line = TruncateDisplay(line, limit)
	}
	_, _ = io.WriteString(p.w, "\r\x1b[2K"+line)
	p.spinnerVisible = true
}

func (p *Progress) termWidthLocked() int {
	if p.opts.Width > 0 {
		return p.opts.Width
	}
	if f, ok := p.w.(*os.File); ok && f != nil {
		if w, _, err := term.GetSize(f.Fd()); err == nil && w > 0 {
			return w
		}
	}
	return 80
}
