package helpers

import (
	"fmt"
)

// Colors for terminal output
const (
	ColorGreen  = "\033[0;32m"
	ColorRed    = "\033[0;31m"
	ColorYellow = "\033[1;33m"
	ColorBlue   = "\033[0;34m"
	ColorReset  = "\033[0m"
)

// LogInfo prints an info message
func LogInfo(msg string) {
	fmt.Printf("%s[INFO]%s %s\n", ColorGreen, ColorReset, msg)
}

// LogError prints an error message
func LogError(msg string) {
	fmt.Printf("%s[ERROR]%s %s\n", ColorRed, ColorReset, msg)
}

// LogTest prints a test message
func LogTest(msg string) {
	fmt.Printf("%s[TEST]%s %s\n", ColorYellow, ColorReset, msg)
}

// LogStep prints a step message
func LogStep(msg string) {
	fmt.Printf("%s[STEP]%s %s\n", ColorBlue, ColorReset, msg)
}

// LogPass prints a pass result
func LogPass(msg string) {
	fmt.Printf("%s[INFO]%s ✓ PASS: %s\n", ColorGreen, ColorReset, msg)
}

// LogFail prints a fail result
func LogFail(msg string) {
	fmt.Printf("%s[ERROR]%s ✗ FAIL: %s\n", ColorRed, ColorReset, msg)
}
