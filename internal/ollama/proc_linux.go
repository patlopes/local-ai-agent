//go:build linux

package ollama

import (
	"os/exec"
	"syscall"
)

// setProcAttr configures the subprocess to use its own process group
// and die when the parent process exits (Linux-only Pdeathsig).
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGTERM,
	}
}

// killProcGroup sends SIGTERM to the entire process group.
func killProcGroup(pid int) error {
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return syscall.Kill(pid, syscall.SIGTERM)
	}
	return syscall.Kill(-pgid, syscall.SIGTERM)
}

// forceKillProcGroup sends SIGKILL to the entire process group.
func forceKillProcGroup(pid int) error {
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return syscall.Kill(pid, syscall.SIGKILL)
	}
	return syscall.Kill(-pgid, syscall.SIGKILL)
}
