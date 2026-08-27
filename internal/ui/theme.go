package ui

import "github.com/charmbracelet/lipgloss"

// Theme holds every style the interface uses.
//
// All styles set colour only — never padding, margins or borders. Widths are
// computed in cells by this package and the frame is drawn by hand, so a style
// that changed a string's width would silently break the alignment of every
// vertical rule on screen.
type Theme struct {
	Border      lipgloss.Style
	BorderFocus lipgloss.Style
	Title       lipgloss.Style
	Header      lipgloss.Style
	Row         lipgloss.Style
	RowAlt      lipgloss.Style
	Cursor      lipgloss.Style
	Selected    lipgloss.Style
	CursorSel   lipgloss.Style
	Dim         lipgloss.Style
	Accent      lipgloss.Style
	Warn        lipgloss.Style
	Error       lipgloss.Style
	OK          lipgloss.Style
	Dirty       lipgloss.Style
	Key         lipgloss.Style
	FieldName   lipgloss.Style
	FieldValue  lipgloss.Style
	Input       lipgloss.Style
	Match       lipgloss.Style
	PopupBorder lipgloss.Style
	PopupItem   lipgloss.Style
	PopupPick   lipgloss.Style
}

// NewTheme builds the default theme. Adaptive colours let the same build look
// right on light and dark terminals without a configuration step.
func NewTheme() Theme {
	var (
		fg       = lipgloss.AdaptiveColor{Light: "#1f2430", Dark: "#e6e9ef"}
		dim      = lipgloss.AdaptiveColor{Light: "#8a8fa3", Dark: "#6c7086"}
		border   = lipgloss.AdaptiveColor{Light: "#c8ccd8", Dark: "#45475a"}
		accent   = lipgloss.AdaptiveColor{Light: "#1e66f5", Dark: "#89b4fa"}
		accent2  = lipgloss.AdaptiveColor{Light: "#8839ef", Dark: "#cba6f7"}
		green    = lipgloss.AdaptiveColor{Light: "#40a02b", Dark: "#a6e3a1"}
		yellow   = lipgloss.AdaptiveColor{Light: "#df8e1d", Dark: "#f9e2af"}
		red      = lipgloss.AdaptiveColor{Light: "#d20f39", Dark: "#f38ba8"}
		selBG    = lipgloss.AdaptiveColor{Light: "#dce4ff", Dark: "#313244"}
		curBG    = lipgloss.AdaptiveColor{Light: "#1e66f5", Dark: "#89b4fa"}
		curFG    = lipgloss.AdaptiveColor{Light: "#ffffff", Dark: "#11111b"}
		panelDim = lipgloss.AdaptiveColor{Light: "#5c6070", Dark: "#9399b2"}
	)

	return Theme{
		Border:      lipgloss.NewStyle().Foreground(border),
		BorderFocus: lipgloss.NewStyle().Foreground(accent),
		Title:       lipgloss.NewStyle().Foreground(accent).Bold(true),
		Header:      lipgloss.NewStyle().Foreground(accent2).Bold(true),
		Row:         lipgloss.NewStyle().Foreground(fg),
		RowAlt:      lipgloss.NewStyle().Foreground(fg),
		Cursor:      lipgloss.NewStyle().Foreground(curFG).Background(curBG).Bold(true),
		Selected:    lipgloss.NewStyle().Foreground(fg).Background(selBG),
		CursorSel:   lipgloss.NewStyle().Foreground(curFG).Background(curBG).Bold(true).Underline(true),
		Dim:         lipgloss.NewStyle().Foreground(dim),
		Accent:      lipgloss.NewStyle().Foreground(accent),
		Warn:        lipgloss.NewStyle().Foreground(yellow),
		Error:       lipgloss.NewStyle().Foreground(red).Bold(true),
		OK:          lipgloss.NewStyle().Foreground(green),
		Dirty:       lipgloss.NewStyle().Foreground(yellow).Bold(true),
		Key:         lipgloss.NewStyle().Foreground(accent).Bold(true),
		FieldName:   lipgloss.NewStyle().Foreground(panelDim),
		FieldValue:  lipgloss.NewStyle().Foreground(fg),
		Input:       lipgloss.NewStyle().Foreground(fg),
		Match:       lipgloss.NewStyle().Foreground(yellow).Bold(true),
		PopupBorder: lipgloss.NewStyle().Foreground(accent2),
		PopupItem:   lipgloss.NewStyle().Foreground(fg),
		PopupPick:   lipgloss.NewStyle().Foreground(curFG).Background(curBG).Bold(true),
	}
}
