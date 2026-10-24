package process

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func Replace(name string, args ...string) error {
	path, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("find service executable %s: %w", name, err)
	}
	if err := syscall.Exec(path, append([]string{name}, args...), os.Environ()); err != nil {
		return fmt.Errorf("execute service %s: %w", name, err)
	}
	return nil
}
