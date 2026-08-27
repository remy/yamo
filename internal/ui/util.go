package ui

import (
	"fmt"
	"os"
)

func sprintf(format string, args ...any) string {
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}

func statFile(path string) (os.FileInfo, error) { return os.Stat(path) }
