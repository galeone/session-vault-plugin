//go:build !windows

package main

import "syscall"

// detachedProcAttr puts the background save in its own session, so it
// outlives the hook and isn't killed with Claude Code's process group.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
