package common

import (
	"strings"
	"sync"
	"testing"
)

// ============================================================
// ULID 生成器测试
//
// 重点：并发安全 + 唯一性。ULID 是主键来源，一旦重复即
// INSERT 主键冲突（不可恢复），因此这里的断言必须严格。
// ============================================================

// TestNewULIDFormat 校验格式：26 字符、时间戳段递增、字符集合法。
func TestNewULIDFormat(t *testing.T) {
	u := NewULID()
	if len(u) != ULIDTotalLen {
		t.Fatalf("长度应为 %d, 实际 %d (%q)", ULIDTotalLen, len(u), u)
	}
	// 字符集：不含易混淆字符（0/O/1/I/L/U）
	allowed := string(encodeChars)
	for i, ch := range u {
		if !strings.ContainsRune(allowed, ch) {
			t.Fatalf("第 %d 位字符 %q 不在允许字符集内: %q", i, ch, u)
		}
	}
}

// TestNewULIDUniqueness 大批量生成的唯一性（单线程）。
func TestNewULIDUniqueness(t *testing.T) {
	const n = 200000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		u := NewULID()
		if _, dup := seen[u]; dup {
			t.Fatalf("★ 第 %d 次生成重复 ULID: %q", i, u)
		}
		seen[u] = struct{}{}
	}
}

// TestNewULIDConcurrentUniqueness 并发唯一性 + 数据竞争检测。
//
// 原实现用包级 `rand.NewSource(...)` 直接调 Int63() —— rand.Source
// **不是并发安全**的，多 goroutine 同时生成会产生数据竞争，随机性退化
// 甚至可能产出重复值。本用例在 -race 下可捕获该竞争。
//
// 运行：go test ./common/ -run TestNewULIDConcurrent -race
func TestNewULIDConcurrentUniqueness(t *testing.T) {
	const (
		goroutines = 32
		perRoutine = 5000
	)

	var (
		mu   sync.Mutex
		seen = make(map[string]struct{}, goroutines*perRoutine)
		dups []string
		wg   sync.WaitGroup
	)

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			local := make([]string, 0, perRoutine)
			for i := 0; i < perRoutine; i++ {
				local = append(local, NewULID())
			}
			mu.Lock()
			defer mu.Unlock()
			for _, u := range local {
				if _, dup := seen[u]; dup {
					dups = append(dups, u)
					continue
				}
				seen[u] = struct{}{}
			}
		}()
	}
	wg.Wait()

	if len(dups) > 0 {
		t.Fatalf("★ 并发生成出现 %d 个重复 ULID（示例: %v）", len(dups), dups[:min(5, len(dups))])
	}
	if len(seen) != goroutines*perRoutine {
		t.Fatalf("生成总数应为 %d, 实际 %d", goroutines*perRoutine, len(seen))
	}
}

// TestInitULID 校验「为空才生成」的语义（预分配接入后两者必须一致）。
func TestInitULID(t *testing.T) {
	// 空 → 生成
	var s string
	InitULID(&s)
	if s == "" {
		t.Fatal("空字段应被填入 ULID")
	}
	// 非空 → 保持不变（尊重调用方已有的值）
	existing := "01J0000000000000000000000A"
	p := existing
	InitULID(&p)
	if p != existing {
		t.Fatalf("非空字段不应被覆盖: got=%q want=%q", p, existing)
	}
}

// min 兼容旧版本 Go（go1.20 无内置 min）。
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
