package ui

import (
	"strings"

	"github.com/mattn/go-runewidth"
)

// input is a single-line text field.
//
// It is hand-rolled rather than borrowed because the layout here is computed
// in exact terminal cells: the field has to report and honour a width in
// cells, scroll horizontally within it, and never emit a string that measures
// differently from what it claims.
type input struct {
	runes  []rune
	cursor int // rune index, 0..len(runes)
	scroll int // rune index of the leftmost visible rune
}

func (in *input) SetValue(s string) {
	in.runes = []rune(s)
	in.cursor = len(in.runes)
	in.scroll = 0
}

func (in *input) Value() string { return string(in.runes) }

func (in *input) Empty() bool { return len(in.runes) == 0 }

func (in *input) Insert(r rune) {
	in.runes = append(in.runes, 0)
	copy(in.runes[in.cursor+1:], in.runes[in.cursor:])
	in.runes[in.cursor] = r
	in.cursor++
}

func (in *input) InsertString(s string) {
	for _, r := range s {
		in.Insert(r)
	}
}

func (in *input) Backspace() {
	if in.cursor == 0 {
		return
	}
	in.runes = append(in.runes[:in.cursor-1], in.runes[in.cursor:]...)
	in.cursor--
}

func (in *input) Delete() {
	if in.cursor >= len(in.runes) {
		return
	}
	in.runes = append(in.runes[:in.cursor], in.runes[in.cursor+1:]...)
}

func (in *input) Left() {
	if in.cursor > 0 {
		in.cursor--
	}
}

func (in *input) Right() {
	if in.cursor < len(in.runes) {
		in.cursor++
	}
}

func (in *input) Home() { in.cursor = 0 }
func (in *input) End()  { in.cursor = len(in.runes) }

// WordLeft moves to the start of the previous word.
func (in *input) WordLeft() {
	for in.cursor > 0 && in.runes[in.cursor-1] == ' ' {
		in.cursor--
	}
	for in.cursor > 0 && in.runes[in.cursor-1] != ' ' {
		in.cursor--
	}
}

// WordRight moves to the start of the next word.
func (in *input) WordRight() {
	n := len(in.runes)
	for in.cursor < n && in.runes[in.cursor] != ' ' {
		in.cursor++
	}
	for in.cursor < n && in.runes[in.cursor] == ' ' {
		in.cursor++
	}
}

// DeleteWordBack removes the word before the cursor.
func (in *input) DeleteWordBack() {
	end := in.cursor
	in.WordLeft()
	in.runes = append(in.runes[:in.cursor], in.runes[end:]...)
}

// DeleteToStart clears everything before the cursor.
func (in *input) DeleteToStart() {
	in.runes = in.runes[in.cursor:]
	in.cursor = 0
}

// DeleteToEnd clears everything from the cursor on.
func (in *input) DeleteToEnd() { in.runes = in.runes[:in.cursor] }

func (in *input) Clear() {
	in.runes = in.runes[:0]
	in.cursor = 0
	in.scroll = 0
}

// Render draws the field in exactly width cells, scrolling so the cursor stays
// visible. When focused, the cell under the cursor is inverted.
func (in *input) Render(width int, focused bool, th Theme, placeholder string) string {
	if width <= 0 {
		return ""
	}
	if len(in.runes) == 0 && !focused && placeholder != "" {
		return th.Dim.Render(Pad(placeholder, width))
	}

	in.reflow(width)

	var b strings.Builder
	used := 0
	i := in.scroll
	for ; i < len(in.runes); i++ {
		r := in.runes[i]
		w := runewidth.RuneWidth(r)
		if w == 0 {
			w = 1
		}
		if used+w > width {
			break
		}
		s := string(r)
		if focused && i == in.cursor {
			b.WriteString(th.Cursor.Render(s))
		} else {
			b.WriteString(th.Input.Render(s))
		}
		used += w
	}
	// The cursor sitting past the last rune needs a cell of its own.
	if focused && in.cursor >= len(in.runes) && used < width {
		b.WriteString(th.Cursor.Render(" "))
		used++
	}
	if used < width {
		b.WriteString(strings.Repeat(" ", width-used))
	}
	return b.String()
}

// reflow adjusts the horizontal scroll so the cursor is inside the window.
func (in *input) reflow(width int) {
	if in.cursor < in.scroll {
		in.scroll = in.cursor
	}
	// Walk back from the cursor until the visible run exceeds the width, which
	// handles double-width runes without a second measurement pass.
	for {
		used := 0
		for i := in.scroll; i < in.cursor; i++ {
			w := runewidth.RuneWidth(in.runes[i])
			if w == 0 {
				w = 1
			}
			used += w
		}
		if used < width || in.scroll >= in.cursor {
			break
		}
		in.scroll++
	}
	if in.scroll > len(in.runes) {
		in.scroll = len(in.runes)
	}
}
