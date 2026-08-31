package ui

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
)

// ImageProtocol is the way a terminal can be asked to draw a bitmap.
type ImageProtocol uint8

const (
	ImageNone   ImageProtocol = iota
	ImageITerm2               // iTerm2's OSC 1337 inline image
	ImageKitty                // Kitty's APC graphics protocol
)

func (p ImageProtocol) String() string {
	switch p {
	case ImageITerm2:
		return "iterm2"
	case ImageKitty:
		return "kitty"
	}
	return "none"
}

// DetectImageProtocol works out whether the terminal can display images.
//
// There is no reliable query for this that does not risk printing rubbish into
// a terminal that does not understand it, so detection is by environment.
// Getting it wrong in the cautious direction costs a preview; getting it wrong
// the other way corrupts the display, so anything uncertain reports none.
func DetectImageProtocol() ImageProtocol {
	if os.Getenv("YAMO_NO_IMAGES") != "" {
		return ImageNone
	}
	// Inside tmux or screen the escape sequences are swallowed or mangled
	// unless passthrough is configured, which cannot be detected from here.
	if os.Getenv("TMUX") != "" || strings.HasPrefix(os.Getenv("TERM"), "screen") {
		return ImageNone
	}

	term := os.Getenv("TERM")
	switch {
	case os.Getenv("KITTY_WINDOW_ID") != "", term == "xterm-kitty":
		return ImageKitty
	case strings.Contains(term, "ghostty"):
		return ImageKitty
	}
	switch os.Getenv("TERM_PROGRAM") {
	case "iTerm.app", "WezTerm":
		return ImageITerm2
	case "ghostty":
		return ImageKitty
	}
	if os.Getenv("LC_TERMINAL") == "iTerm2" {
		return ImageITerm2
	}
	return ImageNone
}

// RenderImage returns the escape sequence that draws data in a box cols wide
// and rows tall, or an empty string if the protocol cannot.
//
// The caller still has to pad the cells the image occupies: to the layout the
// sequence is zero-width, but the terminal will cover that many cells with the
// picture, and the frame has to account for them.
func RenderImage(p ImageProtocol, data []byte, cols, rows int) string {
	if len(data) == 0 || cols <= 0 || rows <= 0 {
		return ""
	}
	switch p {
	case ImageITerm2:
		return iterm2Image(data, cols, rows)
	case ImageKitty:
		return kittyImage(data, cols, rows)
	}
	return ""
}

// iterm2Image builds an OSC 1337 inline image. Width and height are in cells;
// preserveAspectRatio letterboxes rather than distorting a non-square cover.
func iterm2Image(data []byte, cols, rows int) string {
	return fmt.Sprintf("\x1b]1337;File=inline=1;size=%d;width=%d;height=%d;preserveAspectRatio=1:%s\a",
		len(data), cols, rows, base64.StdEncoding.EncodeToString(data))
}

// kittyChunk is the largest base64 payload Kitty accepts in one escape.
const kittyChunk = 4096

// kittyImage builds an APC graphics sequence. Unlike iTerm2's, the payload has
// to be split across several escapes, each flagged as continuing.
func kittyImage(data []byte, cols, rows int) string {
	b64 := base64.StdEncoding.EncodeToString(data)
	var sb strings.Builder
	first := true
	for len(b64) > 0 {
		n := min(len(b64), kittyChunk)
		chunk := b64[:n]
		b64 = b64[n:]
		more := 0
		if len(b64) > 0 {
			more = 1
		}
		if first {
			// a=T transmits and displays; f=100 means the payload is a whole
			// image file rather than raw pixels; c and r size it in cells.
			fmt.Fprintf(&sb, "\x1b_Ga=T,f=100,c=%d,r=%d,m=%d;%s\x1b\\", cols, rows, more, chunk)
			first = false
			continue
		}
		fmt.Fprintf(&sb, "\x1b_Gm=%d;%s\x1b\\", more, chunk)
	}
	return sb.String()
}
