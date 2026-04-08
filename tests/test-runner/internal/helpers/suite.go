package helpers

import "fmt"

// Check represents a single named test/check within a suite.
type Check struct {
	Name string
	Run  func() error
}

// RunChecks executes a suite of checks with consistent logging and summary output.
func RunChecks(suiteName string, checks []Check, verbose bool) (passed, total int) {
	total = len(checks)

	for _, check := range checks {
		LogTest(check.Name)
		if err := check.Run(); err == nil {
			LogPass(check.Name)
			passed++
		} else {
			LogFail(check.Name)
			if verbose {
				LogError(fmt.Sprintf("%s: %v", check.Name, err))
			}
		}
	}

	fmt.Printf("\n%s: %d/%d passed\n", suiteName, passed, total)
	return passed, total
}
