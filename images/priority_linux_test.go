package images

import "syscall"

func niceAsCanBe() bool {
	p, err := syscall.Getpriority(syscall.PRIO_PROCESS, 0)
	return err == nil && p == 1
}
