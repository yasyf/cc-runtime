package runtime

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/shirou/gopsutil/v4/process"
)

const maxHops = 32

var claudePIDOnce = sync.OnceValue(func() int {
	return findAncestor(os.Getppid(), isClaude)
})

// claudePID returns the pid of the nearest claude ancestor of this process,
// resolved once per process. 0 means not inside a Claude window.
func claudePID() int { return claudePIDOnce() }

// live reports whether pid exists and its argv still matches claude, defeating
// pid recycling.
func live(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := process.NewProcess(int32(pid))
	if err != nil {
		return false
	}
	argv, err := p.CmdlineSlice()
	if err != nil {
		return false
	}
	return isClaude(argv)
}

func findAncestor(pid int, match func(argv []string) bool) int {
	return int(walk(int32(pid), match))
}

func walk(pid int32, match func(argv []string) bool) int32 {
	seen := map[int32]bool{}
	for hops := 0; hops < maxHops && pid > 1 && !seen[pid]; hops++ {
		seen[pid] = true
		p, err := process.NewProcess(pid)
		if err != nil {
			return 0
		}
		argv, err := p.CmdlineSlice()
		if err != nil {
			return 0
		}
		if match(argv) {
			return pid
		}
		ppid, err := p.Ppid()
		if err != nil {
			return 0
		}
		pid = ppid
	}
	return 0
}

func isClaude(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	switch filepath.Base(argv[0]) {
	case "claude":
		return true
	case "node", "bun":
		if len(argv) > 1 {
			script := filepath.Base(argv[1])
			return script == "claude" || script == "claude.js"
		}
	}
	return false
}
