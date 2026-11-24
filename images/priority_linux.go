package images

import "syscall"

func lowerPriority() { _ = syscall.Setpriority(syscall.PRIO_PROCESS, 0, 19) }
