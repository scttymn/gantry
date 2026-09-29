package images

import "syscall"

// niceAsCanBe: this process runs at nice 19 (the kernel's getpriority says
// 20 - nice).
func niceAsCanBe() bool {
	p, err := syscall.Getpriority(syscall.PRIO_PROCESS, 0)
	return err == nil && p == 1
}
