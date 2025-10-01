// Package color provides ANSI color support for terminal output.
package color

import "fmt"

// RGB represents an RGB color value
type RGB struct {
	R, G, B uint8
}

// Predefined colors
var (
	WSOrange = RGB{255, 102, 0}   // WebSocket orange (#ff6600)
	TeaGreen = RGB{211, 249, 181} // Tea green (#d3f9b5)
)

// Sprint returns the text with ANSI color codes applied
func (c RGB) Sprint(text string) string {
	return fmt.Sprintf("\033[38;2;%d;%d;%dm%s\033[0m", c.R, c.G, c.B, text)
}

// Apply is an alias for Sprint for consistency
func (c RGB) Apply(text string) string {
	return c.Sprint(text)
}
