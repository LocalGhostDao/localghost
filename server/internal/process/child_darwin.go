package process

import "syscall"

// ChildSysProcAttr puts the child in its own process group. Darwin has no Pdeathsig equivalent;
// lifecycle cleanup is handled by the daemon/supervisor path.
func ChildSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
