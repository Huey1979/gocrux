package repository

import "testing"

// TestMongoPageOptsUnpaged BUG-063 核心：pageSize<=0 必须表示"全量返回"（ok=false，不设 Limit），
// 修复前 listOffset 把 pageSize=0 重置为 20，导致 DoList 级联/Reference 批量展开只返回前 20 条。
func TestMongoPageOptsUnpaged(t *testing.T) {
	// pageSize=0（DoList 级联/Reference 展开传 PageSize: 0 表示全量）
	skip, limit, ok := mongoPageOpts(1, 0, 0)
	if ok {
		t.Fatalf("mongoPageOpts(1, 0, 0) ok=true (skip=%d limit=%d), want ok=false 全量返回", skip, limit)
	}
	// 负数同理（ListFilters 契约：<=0 不分页）
	if _, _, ok := mongoPageOpts(1, -1, 0); ok {
		t.Fatal("mongoPageOpts(1, -1, 0) ok=true, want ok=false 全量返回")
	}
}

// TestMongoPageOptsPaged 兼容验证：pageSize>0 时正常分页，offset 优先于 page，page<1 归一。
func TestMongoPageOptsPaged(t *testing.T) {
	// 标准分页：page=3, pageSize=20 → skip=40
	skip, limit, ok := mongoPageOpts(3, 20, 0)
	if !ok || skip != 40 || limit != 20 {
		t.Fatalf("mongoPageOpts(3, 20, 0) = skip=%d limit=%d ok=%v, want skip=40 limit=20 ok=true", skip, limit, ok)
	}
	// HTTP 入口 page_size=50 不受影响（heims notify-delivery/list?page_size=50 场景）
	skip, limit, ok = mongoPageOpts(1, 50, 0)
	if !ok || skip != 0 || limit != 50 {
		t.Fatalf("mongoPageOpts(1, 50, 0) = skip=%d limit=%d ok=%v, want skip=0 limit=50 ok=true", skip, limit, ok)
	}
	// offset 优先于 page：page=3 但 offset=100 → skip=100
	skip, limit, ok = mongoPageOpts(3, 20, 100)
	if !ok || skip != 100 || limit != 20 {
		t.Fatalf("mongoPageOpts(3, 20, 100) = skip=%d limit=%d ok=%v, want skip=100 limit=20 ok=true", skip, limit, ok)
	}
	// page<1 归一为第 1 页
	skip, limit, ok = mongoPageOpts(0, 20, 0)
	if !ok || skip != 0 || limit != 20 {
		t.Fatalf("mongoPageOpts(0, 20, 0) = skip=%d limit=%d ok=%v, want skip=0 limit=20 ok=true", skip, limit, ok)
	}
}
