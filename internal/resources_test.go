package moerise

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"runtime"
	"runtime/debug"
	"testing"
	"testing/synctest"
	"time"
)

// 文件夹具覆盖单台 Docker 主机难以可靠复现的挂载命名空间和父级配额。
func TestResourceLimitsAcrossCgroupLayouts(t *testing.T) {
	for _, tc := range []struct {
		name, groups, mounts string
		files                map[string]string
		cpus                 int
		memory               int64
	}{
		{"native", "", "", nil, runtime.NumCPU(), 1024 * mebibyte},
		{"v2-parent", "0::/services/core\n", "1 0 0:1 / /cg rw - cgroup2 cgroup rw\n",
			map[string]string{"/cg/services/core/memory.max": "max", "/cg/services/memory.max": "402653184",
				"/cg/services/core/cpu.max": "max 100000", "/cg/services/cpu.max": "100000 100000"},
			1, 384 * mebibyte},
		{"v2-namespace", "0::/\n", "1 0 0:1 /docker/core /cg rw - cgroup2 cgroup rw\n",
			map[string]string{"/cg/memory.max": "268435456", "/cg/cpu.max": "50000 100000"},
			1, 256 * mebibyte},
		{"v1", "2:cpu,cpuacct:/core\n3:memory:/core\n",
			"1 0 0:1 / /cg/cpu rw - cgroup cgroup rw,cpu,cpuacct\n2 0 0:2 / /cg/memory rw - cgroup cgroup rw,memory\n",
			map[string]string{"/cg/memory/core/memory.limit_in_bytes": "536870912",
				"/cg/cpu/core/cpu.cfs_quota_us": "200000", "/cg/cpu/core/cpu.cfs_period_us": "100000"},
			min(2, runtime.NumCPU()), 512 * mebibyte},
		{"v1-unbounded", "3:memory:/core\n", "2 0 0:2 / /cg/memory rw - cgroup cgroup rw,memory\n",
			map[string]string{"/cg/memory/core/memory.limit_in_bytes": "9223372036854771712"},
			runtime.NumCPU(), 1024 * mebibyte},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{"/proc/meminfo": "MemTotal: 8388608 kB\n",
				"/proc/self/cgroup": tc.groups, "/proc/self/mountinfo": tc.mounts}
			for path, data := range tc.files {
				files[path] = data
			}
			got := detectResources(func(path string) ([]byte, error) {
				if data, ok := files[path]; ok {
					return []byte(data), nil
				}
				return nil, fs.ErrNotExist
			})
			if got.CPUs != tc.cpus || got.MemoryBytes != tc.memory {
				t.Fatalf("got %+v, want CPUs=%d memory=%d", got, tc.cpus, tc.memory)
			}
		})
	}
}

func TestRuntimeMemoryBudgetAndExplicitOverride(t *testing.T) {
	old := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(old) })
	t.Setenv("GOMEMLIMIT", "")
	automatic := ConfigureRuntime()
	if automatic.GoMemoryBytes != processResources.MemoryBytes/3*2 {
		t.Fatalf("automatic budget: %+v", automatic)
	}
	for _, value := range []string{"96MiB", "off"} {
		t.Setenv("GOMEMLIMIT", value)
		explicit := int64(96 * mebibyte)
		if value == "off" {
			explicit = 1<<63 - 1
		}
		debug.SetMemoryLimit(explicit)
		if got := ConfigureRuntime(); got.GoMemoryBytes != explicit {
			t.Fatalf("overrode GOMEMLIMIT=%s: %+v", value, got)
		}
	}
}

func TestBackgroundAndSourceQueuesRespectCancellation(t *testing.T) {
	settings := testSettings(t)
	store := testStore(t, settings)
	source := fakeClient(settings, func(*http.Request) (int, any, error) {
		t.Error("cancelled work reached Memos")
		return 500, nil, nil
	})
	unlock, err := store.lockBackground(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	for _, run := range []func(context.Context) error{
		func(ctx context.Context) error { _, err := NewSyncService(store, source).Sync(ctx, Job{}); return err },
		func(ctx context.Context) error {
			_, err := NewVectorIndexService(store, source, nil).Step(ctx)
			return err
		},
	} {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		err := run(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("background queue lost deadline: %v", err)
		}
	}
	for range cap(source.slots) {
		source.slots <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := source.Get(ctx, "memos/one"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("source queue lost deadline: %v", err)
	}
}

// 虚拟调度保证等待者已进入队列，避免靠真实时钟碰运气复现饥饿。
func TestBackgroundQueueHandsOffBeforeNextBatch(t *testing.T) {
	store := testStore(t, testSettings(t))
	synctest.Test(t, func(t *testing.T) {
		release, err := store.lockBackground(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		order := make(chan string, 2)
		go func() {
			unlock, err := store.lockBackground(t.Context())
			if err != nil {
				t.Error(err)
				return
			}
			order <- "sync"
			unlock()
		}()
		synctest.Wait()
		ctx, cancel := context.WithCancel(t.Context())
		go func() {
			unlock, err := store.lockBackground(ctx)
			if err == nil {
				unlock()
				t.Error("canceled waiter acquired background slot")
			} else if !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		}()
		synctest.Wait()
		cancel()
		synctest.Wait()
		release()
		next, err := store.lockBackground(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		order <- "next vector batch"
		next()
		if first, second := <-order, <-order; first != "sync" || second != "next vector batch" {
			t.Fatalf("background work overtook its waiter: %q, %q", first, second)
		}
	})
}
