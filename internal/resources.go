package moerise

import (
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
)

const mebibyte = int64(1 << 20)

// RuntimeResources 包含进程及所属 cgroup 的有效资源预算。
type RuntimeResources struct {
	CPUs          int   `json:"cpus"`
	MemoryBytes   int64 `json:"memory_bytes"`
	GoMemoryBytes int64 `json:"go_memory_bytes"`
}

var processResources = detectResources(os.ReadFile)

func positiveLimit(text string) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	if err != nil || value <= 0 || value >= 1<<62 {
		return 0
	}
	return value
}

// detectResources 读取两种 cgroup 布局及可见父级限制。
// 无配额的原生进程使用四分之一物理内存作为预算，最多 1 GiB。
func detectResources(read func(string) ([]byte, error)) RuntimeResources {
	result := RuntimeResources{CPUs: runtime.NumCPU()}
	meminfo, _ := read("/proc/meminfo")
	var physical int64
	for _, line := range strings.Split(string(meminfo), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "MemTotal:" && fields[2] == "kB" {
			physical = positiveLimit(fields[1]) * 1024
		}
	}
	groups, _ := read("/proc/self/cgroup")
	membership := map[string]string{}
	for _, line := range strings.Split(string(groups), "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) == 3 {
			for _, controller := range strings.Split(fields[1], ",") {
				membership[controller] = fields[2]
			}
		}
	}
	mountinfo, _ := read("/proc/self/mountinfo")
	var memory int64
	for _, line := range strings.Split(string(mountinfo), "\n") {
		before, after, ok := strings.Cut(line, " - ")
		fields, fs := strings.Fields(before), strings.Fields(after)
		if !ok || len(fields) < 5 || len(fs) < 3 || (fs[0] != "cgroup" && fs[0] != "cgroup2") {
			continue
		}
		unescape := func(value string) string {
			return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(value)
		}
		root, mount := unescape(fields[3]), unescape(fields[4])
		controllers := strings.Split(fs[2], ",")
		if fs[0] == "cgroup2" {
			controllers = []string{""}
		}
		for _, controller := range controllers {
			if controller != "" && controller != "memory" && controller != "cpu" {
				continue
			}
			group, exists := membership[controller]
			if !exists {
				continue
			}
			relative := group
			if root != "/" && (group == root || strings.HasPrefix(group, root+"/")) {
				relative = strings.TrimPrefix(group, root)
			}
			path := filepath.Join(mount, filepath.Clean("/"+relative))
			for {
				if controller == "" || controller == "memory" {
					file := "memory.max"
					if controller == "memory" {
						file = "memory.limit_in_bytes"
					}
					data, _ := read(filepath.Join(path, file))
					if limit := positiveLimit(string(data)); limit > 0 && (memory == 0 || limit < memory) {
						memory = limit
					}
				}
				if controller == "" || controller == "cpu" {
					var quota, period int64
					if controller == "" {
						data, _ := read(filepath.Join(path, "cpu.max"))
						values := strings.Fields(string(data))
						if len(values) == 2 {
							quota, period = positiveLimit(values[0]), positiveLimit(values[1])
						}
					} else {
						q, _ := read(filepath.Join(path, "cpu.cfs_quota_us"))
						p, _ := read(filepath.Join(path, "cpu.cfs_period_us"))
						quota, period = positiveLimit(string(q)), positiveLimit(string(p))
					}
					if quota > 0 && period > 0 {
						result.CPUs = min(result.CPUs, int((quota-1)/period+1))
					}
				}
				if path == mount {
					break
				}
				parent := filepath.Dir(path)
				if parent == path || (parent != mount && !strings.HasPrefix(parent, mount+"/")) {
					break
				}
				path = parent
			}
		}
	}
	if memory > 0 {
		result.MemoryBytes = memory
		if physical > 0 {
			result.MemoryBytes = min(memory, physical)
		}
	} else if physical > 0 {
		result.MemoryBytes = min(physical/4, 1024*mebibyte)
	} else {
		result.MemoryBytes = 256 * mebibyte
	}
	result.CPUs = max(1, result.CPUs)
	return result
}

// ConfigureRuntime 优先使用显式 GOMEMLIMIT，包括 GOMEMLIMIT=off。
func ConfigureRuntime() RuntimeResources {
	result := processResources
	result.CPUs = processCPUs()
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(result.MemoryBytes / 3 * 2)
	}
	result.GoMemoryBytes = debug.SetMemoryLimit(-1)
	return result
}

func processCPUs() int {
	return max(1, min(runtime.GOMAXPROCS(0), processResources.CPUs))
}

func sourcePageSize(configured int) int {
	if configured > 0 {
		return configured
	}
	budget := min(processResources.MemoryBytes, debug.SetMemoryLimit(-1))
	switch {
	case budget >= 128*mebibyte:
		return 1000
	case budget >= 64*mebibyte:
		return 500
	default:
		return 100
	}
}
