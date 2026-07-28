package process

import "syscall"

// ChildSysProcAttr puts the child in its own process group and asks the kernel to kill it if the
// parent process dies before it can clean up.
func ChildSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
