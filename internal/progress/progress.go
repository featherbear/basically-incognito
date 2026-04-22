// Package progress provides a simple single-line terminal progress bar.
package progress

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
)

const barWidth = 30

// Bar is a thread-safe terminal progress bar.
type Bar struct {
	total   int
	current atomic.Int64
	label   string
}

// New creates a new Bar with the given total and label.
func New(total int, label string) *Bar {
	return &Bar{total: total, label: label}
}

// Inc increments the progress counter by 1 and redraws.
func (b *Bar) Inc() {
	b.current.Add(1)
	b.draw()
}

// Done finalises the bar with a newline.
func (b *Bar) Done() {
	b.draw()
	fmt.Fprintln(os.Stderr)
}

func (b *Bar) draw() {
	cur := int(b.current.Load())
	pct := 0.0
	if b.total > 0 {
		pct = float64(cur) / float64(b.total)
	}
	filled := int(pct * barWidth)
	if filled > barWidth {
		filled = barWidth
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
	fmt.Fprintf(os.Stderr, "\r  %s [%s] %d/%d", b.label, bar, cur, b.total)
}
