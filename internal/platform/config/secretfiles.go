package config

import (
	"fmt"
	"os"
	"strings"
)

// LoadSecretFiles sets each key from the file named by KEY_FILE, following the
// Docker convention, so that secrets need not be passed as environment values.
func LoadSecretFiles(keys ...string) error {
	for _, key := range keys {
		path := os.Getenv(key + "_FILE")
		if path == "" {
			continue
		}
		if os.Getenv(key) != "" {
			return fmt.Errorf("set either %s or %s_FILE, not both", key, key)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s_FILE: %w", key, err)
		}
		value := strings.TrimRight(string(contents), "\r\n")
		if value == "" {
			return fmt.Errorf("%s_FILE %s is empty", key, path)
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
		if err := os.Unsetenv(key + "_FILE"); err != nil {
			return err
		}
	}
	return nil
}
