package network

import (
	"fmt"
	"os/exec"
	"strings"
)

// ListSMBShares authenticates to the server and returns the advertised SMB shares.
func ListSMBShares(host, port, username, password, workgroup string) (string, error) {
	args := []string{"-m", "SMB3", "-g", "-L", fmt.Sprintf("//%s", host), "-U", username, "--password=" + password}
	if workgroup != "" {
		args = append(args, "-W", workgroup)
	}
	if port != "" {
		args = append(args, "-p", port)
	}

	cmd := exec.Command("smbclient", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("listing SMB shares failed: %w\n%s", err, strings.TrimSpace(string(output)))
	}

	return string(output), nil
}

// ReadSMBShare lists the contents of a share to confirm authenticated access works.
func ReadSMBShare(host, port, share, username, password, workgroup string) (string, error) {
	args := []string{fmt.Sprintf("//%s/%s", host, share), "-m", "SMB3", "-U", username, "--password=" + password, "-c", "ls"}
	if workgroup != "" {
		args = append(args, "-W", workgroup)
	}
	if port != "" {
		args = append(args, "-p", port)
	}

	cmd := exec.Command("smbclient", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("reading SMB share %s failed: %w\n%s", share, err, strings.TrimSpace(string(output)))
	}

	return string(output), nil
}
