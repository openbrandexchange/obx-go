package main

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// bar is a progress bar on a terminal ("images [██████░░░░] 60% 31/52 ·
// 8 MB · about 12 s left") and a line every few seconds otherwise (log files,
// CI), so long runs never look stuck.
type bar struct {
	mu      sync.Mutex
	w       io.Writer
	tty     bool
	label   string
	total   int64
	start   time.Time
	last    time.Time
	width   int
	every   time.Duration
	printed bool
	e       *env
}

func newBar(e *env, w io.Writer, tty bool, label string, total int64) *bar {
	return &bar{w: w, tty: tty, label: label, total: total, start: time.Now(), every: 5 * time.Second, e: e}
}

// set shows done of total, with extra text after the numbers.
func (b *bar) set(done int64, extra string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	if !b.tty {
		if now.Sub(b.last) < b.every && done < b.total {
			return
		}
		b.last = now
	}
	line := b.render(done, extra, now)
	if b.tty {
		pad := ""
		if n := b.width - len([]rune(line)); n > 0 {
			pad = strings.Repeat(" ", n)
		}
		fmt.Fprintf(b.w, "\r%s%s", line, pad)
		b.width = len([]rune(line))
	} else {
		fmt.Fprintln(b.w, line)
	}
	b.printed = true
}

func (b *bar) render(done int64, extra string, now time.Time) string {
	var sb strings.Builder
	sb.WriteString("  " + b.label + " ")
	if b.total > 0 {
		const cells = 24
		frac := float64(done) / float64(b.total)
		frac = min(max(frac, 0), 1)
		full := int(frac * cells)
		if b.tty {
			sb.WriteString("[" + strings.Repeat("█", full) + strings.Repeat("░", cells-full) + "] ")
		}
		fmt.Fprintf(&sb, "%3d%%  %d/%d", int(frac*100), done, b.total)
		elapsed := now.Sub(b.start)
		if done > 0 && done < b.total && elapsed > 2*time.Second {
			left := time.Duration(float64(elapsed) * float64(b.total-done) / float64(done))
			sb.WriteString("  ·  " + b.e.t("progress.left", "time", humanDuration(left)))
		}
	} else {
		fmt.Fprintf(&sb, "%d", done)
	}
	if extra != "" {
		sb.WriteString("  ·  " + extra)
	}
	return sb.String()
}

// done replaces the bar with a final line.
func (b *bar) done(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tty {
		pad := ""
		if n := b.width - len([]rune(line)); n > 0 {
			pad = strings.Repeat(" ", n)
		}
		fmt.Fprintf(b.w, "\r%s%s\n", line, pad)
		b.width = 0
		return
	}
	fmt.Fprintln(b.w, line)
}

// clear removes a bar from the terminal without a final line.
func (b *bar) clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tty && b.printed {
		fmt.Fprintf(b.w, "\r%s\r", strings.Repeat(" ", b.width))
		b.width = 0
	}
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()+0.5))
	case d < time.Hour:
		return fmt.Sprintf("%d:%02d min", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%d:%02d h", int(d.Hours()), int(d.Minutes())%60)
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f kB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

var _ = register(map[string]string{
	"progress.left": "about {time} left",
})
