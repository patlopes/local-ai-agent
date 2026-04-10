//go:build windows

package ollama

import (
	"os"
	"os/exec"
)

func setProcAttr(cmd *exec.Cmd) {
	// No special process attributes on Windows
}

func killProcGroup(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

func forceKillProcGroup(pid int) error {
	return killProcGroup(pid)
}
