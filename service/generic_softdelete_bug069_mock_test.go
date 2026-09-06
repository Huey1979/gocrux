package service

import (
	"context"
	"reflect"
	"testing"

	"gorm.io/gorm"

	errs "github.com/Huey1979/gocrux/errors"
	"github.com/Huey1979/gocrux/repository"
)

// ============================================================
// BUG-069 回归测试（二）：mock repo 纯单元级（不依赖数据库）
//
// 覆盖 helper 语义本身：
//   - deletedCol / softDeleteFilter 的配置解析（含 SetDelete=false 与自定义配置）
//   - isSoftDeleted 判定
//   - _doGet 对已删记录返回 ErrRecordNotFound
//   - filterLiveIDs 的过滤条件内容与剔除结果
//   - BatchUpdateByIDs 全已删 → 不触碰 repo（与 BUG-052 空 ids 静默成功语义一致）
//   - 无软删列实体不产生额外查询、不加过滤条件
// ============================================================

// bug069MockRepo 通用 mock 仓储：按 ListFilters 在内存 rows 上做最小过滤。
type bug069MockRepo[M Record] struct {
	rows        []M
	byID        M
	byIDErr     error
	lastFilters repository.ListFilters
	lastIDs     []any
	updateCalls int
	listCalls   int
}

func (m *bug069MockRepo[M]) GetByID(_ context.Context, _ any) (*M, error) {
	if m.byIDErr != nil {
		return nil, m.byIDErr
	}
	if reflect.ValueOf(m.byID).IsZero() {
		return nil, gorm.ErrRecordNotFound
	}
	return &m.byID, nil
}

func (m *bug069MockRepo[M]) ListByFilters(_ context.Context, f repository.ListFilters) ([]M, int64, error) {
	m.listCalls++
	m.lastFilters = f
	var out []M
	for _, r := range m.rows {
		if bug069MatchFilters[M](r, f.Filters) {
			out = append(out, r)
		}
	}
	return out, int64(len(out)), nil
}

func (m *bug069MockRepo[M]) UpdateByIDs(_ context.Context, ids []any, _ map[string]any) error {
	m.updateCalls++
	m.lastIDs = ids
	return nil
}

func (m *bug069MockRepo[M]) Insert(context.Context, *M, ...string) error        { panic("unused") }
func (m *bug069MockRepo[M]) InsertBatch(context.Context, []*M, ...string) error { panic("unused") }
func (m *bug069MockRepo[M]) GetByField(context.Context, string, any) (*M, error) {
	panic("unused")
}
func (m *bug069MockRepo[M]) Save(context.Context, *M) error                        { panic("unused") }
func (m *bug069MockRepo[M]) UpdateByID(context.Context, any, map[string]any) error { panic("unused") }
func (m *bug069MockRepo[M]) Delete(context.Context, any) error                     { panic("unused") }
func (m *bug069MockRepo[M]) DeleteByFK(context.Context, string, []any) error       { panic("unused") }
func (m *bug069MockRepo[M]) BatchSoftDelete(context.Context, []any) error          { panic("unused") }
func (m *bug069MockRepo[M]) BatchSoftDeleteByFK(context.Context, string, []any) error {
	panic("unused")
}
func (m *bug069MockRepo[M]) BatchFindByPK(context.Context, []any) ([]M, error) { panic("unused") }
func (m *bug069MockRepo[M]) BatchFindByFK(context.Context, string, []any) ([]M, error) {
	panic("unused")
}
func (m *bug069MockRepo[M]) BatchHardDelete(context.Context, []any) error { panic("unused") }
func (m *bug069MockRepo[M]) BatchHardDeleteByFK(context.Context, string, []any) error {
	panic("unused")
}
func (m *bug069MockRepo[M]) BatchDeprecateVersions(context.Context, []any) error { panic("unused") }
func (m *bug069MockRepo[M]) BatchDeprecateVersionsByFK(context.Context, string, []any) error {
	panic("unused")
}
func (m *bug069MockRepo[M]) ListAll(context.Context) ([]M, error) { panic("unused") }
func (m *bug069MockRepo[M]) ListByField(context.Context, string, any) ([]M, error) {
	panic("unused")
}
func (m *bug069MockRepo[M]) RawList(context.Context, any, any, ...any) error { panic("unused") }
func (m *bug069MockRepo[M]) RunInTx(context.Context, func(context.Context) error) error {
	panic("unused")
}
func (m *bug069MockRepo[M]) PKField() string { return "ulid" }

// bug069MatchFilters 在内存行上按列名匹配过滤条件（仅实现测试用到的 EQ / IN）。
func bug069MatchFilters[M Record](row M, filters []repository.Filter) bool {
	for _, f := range filters {
		goField := resolveColumnFromDB[M](f.Field)
		if goField == "" {
			continue
		}
		v := getFieldVal(row, goField)
		switch f.Op {
		case repository.OpEQ:
			if !sameSoftDeleteValue(v, f.Value) {
				return false
			}
		case repository.OpIn:
			vals, ok := f.Value.([]any)
			if !ok {
				return false
			}
			hit := false
			for _, x := range vals {
				if sameSoftDeleteValue(v, x) {
					hit = true
					break
				}
			}
			if !hit {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// TestBug069DeletedColSemantics helper 配置解析：
// SetDelete=false → 不支持软删（不得拼过滤条件）；默认 is_deleted/int8(0)；
// 自定义 DeletedField/DeletedValue 原样生效。
func TestBug069DeletedColSemantics(t *testing.T) {
	// 无软删列实体
	svcNo := &GenericService[*bug069NoDelDoc]{}
	if _, _, ok := svcNo.deletedCol(); ok {
		t.Error("SetDelete()=false must report ok=false (no soft-delete column)")
	}
	if _, ok := svcNo.softDeleteFilter(); ok {
		t.Error("SetDelete()=false must not produce a soft-delete filter")
	}
	noDelRow := &bug069NoDelDoc{ULID: "u", Name: "n"}
	if svcNo.isSoftDeleted(&noDelRow) {
		t.Error("entity without soft-delete column must never be treated as deleted")
	}

	// 默认配置
	svc := &GenericService[*bug069Doc]{}
	field, val, ok := svc.deletedCol()
	if !ok || field != "is_deleted" || val != int8(0) {
		t.Errorf("deletedCol() = (%q, %v, %v), want (is_deleted, 0, true)", field, val, ok)
	}
	delRow := &bug069Doc{ULID: "u", IsDeleted: 1}
	if !svc.isSoftDeleted(&delRow) {
		t.Error("is_deleted=1 must be detected as deleted")
	}
	liveRow := &bug069Doc{ULID: "u"}
	if svc.isSoftDeleted(&liveRow) {
		t.Error("is_deleted=0 must be treated as live")
	}

	// 自定义配置
	svcCustom := &GenericService[*bug069CustomDoc]{
		config: Config[*bug069CustomDoc]{DeletedField: "deleted", DeletedValue: "n"},
	}
	field, val, ok = svcCustom.deletedCol()
	if !ok || field != "deleted" || val != "n" {
		t.Errorf("custom deletedCol() = (%q, %v, %v), want (deleted, n, true)", field, val, ok)
	}
	f, ok := svcCustom.softDeleteFilter()
	if !ok || f.Field != "deleted" || f.Op != repository.OpEQ || f.Value != "n" {
		t.Errorf("softDeleteFilter() = %+v, want {deleted eq n}", f)
	}
	customDel := &bug069CustomDoc{ULID: "u", Deleted: "y"}
	if !svcCustom.isSoftDeleted(&customDel) {
		t.Error("custom deleted='y' must be detected as deleted")
	}
	customLive := &bug069CustomDoc{ULID: "u", Deleted: "n"}
	if svcCustom.isSoftDeleted(&customLive) {
		t.Error("custom deleted='n' must be treated as live")
	}
}

// TestBug069DoGetRejectsDeletedRecord _doGet 对已删记录返回 ErrRecordNotFound。
func TestBug069DoGetRejectsDeletedRecord(t *testing.T) {
	ctx := context.Background()

	deleted := &bug069MockRepo[*bug069Doc]{byID: &bug069Doc{ULID: "u1", Name: "x", IsDeleted: 1}}
	svcDeleted := &GenericService[*bug069Doc]{repo: deleted}
	if _, err := svcDeleted._doGet(ctx, "u1"); err != errs.ErrRecordNotFound {
		t.Errorf("BUG-069: _doGet on deleted record = %v, want ErrRecordNotFound", err)
	}

	live := &bug069MockRepo[*bug069Doc]{byID: &bug069Doc{ULID: "u2", Name: "ok"}}
	svcLive := &GenericService[*bug069Doc]{repo: live}
	got, err := svcLive._doGet(ctx, "u2")
	if err != nil {
		t.Fatalf("_doGet on live record: %v", err)
	}
	if (*got).Name != "ok" {
		t.Errorf("name = %q, want ok", (*got).Name)
	}

	// 不存在 → 仍是 ErrRecordNotFound（不回归）
	missing := &bug069MockRepo[*bug069Doc]{}
	svcMissing := &GenericService[*bug069Doc]{repo: missing}
	if _, err := svcMissing._doGet(ctx, "nope"); err != errs.ErrRecordNotFound {
		t.Errorf("_doGet on missing record = %v, want ErrRecordNotFound", err)
	}
}

// TestBug069FilterLiveIDsExcludesDeleted filterLiveIDs：
// 过滤条件含软删列，返回的 id 剔除已删记录。
func TestBug069FilterLiveIDsExcludesDeleted(t *testing.T) {
	repo := &bug069MockRepo[*bug069Doc]{rows: []*bug069Doc{
		{ULID: "a", IsDeleted: 1},
		{ULID: "b"},
		{ULID: "c"},
	}}
	svc := &GenericService[*bug069Doc]{repo: repo}

	ids, err := svc.filterLiveIDs(context.Background(), []any{"a", "b", "c"})
	if err != nil {
		t.Fatalf("filterLiveIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("live ids = %v, want [b c]", ids)
	}
	if ids[0] != "b" || ids[1] != "c" {
		t.Errorf("live ids = %v, want [b c]", ids)
	}

	// 过滤条件必须包含软删列与主键 IN
	foundDel, foundPK := false, false
	for _, f := range repo.lastFilters.Filters {
		if f.Field == "is_deleted" && f.Op == repository.OpEQ {
			foundDel = true
		}
		if f.Field == "ulid" && f.Op == repository.OpIn {
			foundPK = true
		}
	}
	if !foundDel || !foundPK {
		t.Errorf("filters must contain is_deleted EQ + ulid IN, got %+v", repo.lastFilters.Filters)
	}
	if repo.lastFilters.PageSize != 0 {
		t.Errorf("PageSize = %d, want 0 (full result, no pagination)", repo.lastFilters.PageSize)
	}
}

// TestBug069BatchUpdateAllDeletedNoOp 全部 id 已删 → 无操作成功，不触碰 repo
// （与 BUG-052「空 ids 静默成功」语义一致）。
func TestBug069BatchUpdateAllDeletedNoOp(t *testing.T) {
	repo := &bug069MockRepo[*bug069Doc]{rows: []*bug069Doc{{ULID: "a", IsDeleted: 1}}}
	svc := &GenericService[*bug069Doc]{repo: repo}

	if err := svc.BatchUpdateByIDs(context.Background(), []any{"a"}, map[string]any{"name": "x"}); err != nil {
		t.Fatalf("BatchUpdateByIDs on all-deleted ids: %v", err)
	}
	if repo.updateCalls != 0 {
		t.Errorf("all-deleted ids must not reach repo.UpdateByIDs, calls = %d", repo.updateCalls)
	}
}

// TestBug069NoSoftDeleteColumnNoExtraQuery 无软删列实体：
// filterLiveIDs 原样返回 ids，不产生额外查询（避免给无该列的表拼条件）。
func TestBug069NoSoftDeleteColumnNoExtraQuery(t *testing.T) {
	repo := &bug069MockRepo[*bug069NoDelDoc]{}
	svc := &GenericService[*bug069NoDelDoc]{repo: repo}

	ids, err := svc.filterLiveIDs(context.Background(), []any{"a", "b"})
	if err != nil {
		t.Fatalf("filterLiveIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("ids = %v, want unchanged [a b]", ids)
	}
	if repo.listCalls != 0 {
		t.Errorf("no soft-delete column must not trigger extra query, listCalls = %d", repo.listCalls)
	}
}
