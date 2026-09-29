package images

import "syscall"

// lowerPriority makes this process the last to get the CPU (nice 19), so a
// child's resizing never slows the server's requests.
func lowerPriority() { syscall.Setpriority(syscall.PRIO_PROCESS, 0, 19) }
