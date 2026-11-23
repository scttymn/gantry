package images

import "syscall"

// lowerPriority makes this process the last to get the CPU (nice 19), so a
// child's resizing never slows the server's requests. Where the kernel
// refuses (a sandbox that forbids it), resizing runs at the priority it has.
func lowerPriority() { _ = syscall.Setpriority(syscall.PRIO_PROCESS, 0, 19) }
