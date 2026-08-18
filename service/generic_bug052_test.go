package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Huey1979/gocrux/repository"

	errs "github.com/Huey1979/gocrux/errors"
)

// bug052MockRepo 记录 UpdateByIDs 调用，其余 Repo 方法未使用（panic 兜底防误用）。
type bug052MockRepo struct {
	callCount int
	lastIDs   []any
	lastMap   map[string]any
}

func (m *bug052MockRepo) UpdateByIDs(_ context.Context, ids []any, updates map[string]any) error {
	m.callCount++
	m.lastIDs = ids
	m.lastMap = updates
	return nil
}

func (m *bug052MockRepo) Insert(context.Context, **bug045Doc, ...string) error       { panic("unused") }
func (m *bug052MockRepo) InsertBatch(context.Context, []**bug045Doc, ...string) error { panic("unused") }
func (m *bug052MockRepo) GetByID(context.Context, any) (**bug045Doc, error)          { panic("unused") }
func (m *bug052MockRepo) GetByField(context.Context, string, any) (**bug045Doc, error) {
	panic("unused")
}
func (m *bug052MockRepo) Save(context.Context, **bug045Doc) error        { panic("unused") }
func (m *bug052MockRepo) UpdateByID(context.Context, any, map[string]any) error { panic("unused") }
func (m *bug052MockRepo) Delete(context.Context, any) error             { panic("unused") }
func (m *bug052MockRepo) DeleteByFK(context.Context, string, []any) error { panic("unused") }
func (m *bug052MockRepo) BatchSoftDelete(context.Context, []any) error  { panic("unused") }
func (m *bug052MockRepo) BatchSoftDeleteByFK(context.Context, string, []any) error {
	panic("unused")
}
func (m *bug052MockRepo) BatchFindByPK(context.Context, []any) ([]*bug045Doc, error) {
	panic("unused")
}
func (m *bug052MockRepo) BatchFindByFK(context.Context, string, []any) ([]*bug045Doc, error) {
	panic("unused")
}
func (m *bug052MockRepo) BatchHardDelete(context.Context, []any) error { panic("unused") }
func (m *bug052MockRepo) BatchHardDeleteByFK(context.Context, string, []any) error {
	panic("unused")
}
func (m *bug052MockRepo) BatchDeprecateVersions(context.Context, []any) error { panic("unused") }
func (m *bug052MockRepo) BatchDeprecateVersionsByFK(context.Context, string, []any) error {
	panic("unused")
}
func (m *bug052MockRepo) ListByFilters(context.Context, repository.ListFilters) ([]*bug045Doc, int64, error) {
	panic("unused")
}
func (m *bug052MockRepo) ListAll(context.Context) ([]*bug045Doc, error) { panic("unused") }
func (m *bug052MockRepo) ListByField(context.Context, string, any) ([]*bug045Doc, error) {
	panic("unused")
}
func (m *bug052MockRepo) RawList(context.Context, any, any, ...any) error { panic("unused") }
func (m *bug052MockRepo) RunInTx(context.Context, func(context.Context) error) error {
	panic("unused")
}
func (m *bug052MockRepo) PKField() string { return "id" }

// TestBatchUpdateByIDsEmptyIDsNoOp 验证 BUG-052：
// 空 ids（nil 或空切片，可能来自 BeforeBatchUpdate hook 权限过滤后清空，
// 如 heims notify-delivery 非接收人标记已读）应无操作返回 nil，
// 不报「缺少必需参数: ids」，也不触碰 repo。
func TestBatchUpdateByIDsEmptyIDsNoOp(t *testing.T) {
	repo := &bug052MockRepo{}
	svc := &GenericService[*bug045Doc]{repo: repo}

	if err := svc.BatchUpdateByIDs(context.Background(), nil, map[string]any{"name": "x"}); err != nil {
		t.Fatalf("BUG-052: nil ids should be no-op success, got %v", err)
	}
	if err := svc.BatchUpdateByIDs(context.Background(), []any{}, map[string]any{"name": "x"}); err != nil {
		t.Fatalf("BUG-052: empty ids should be no-op success, got %v", err)
	}
	if repo.callCount != 0 {
		t.Errorf("empty ids must not reach repo, callCount=%d", repo.callCount)
	}
}

// TestBatchUpdateByIDsVersionModeUnchanged 版本化模式检查仍在 ids 校验之前，行为不变。
func TestBatchUpdateByIDsVersionModeUnchanged(t *testing.T) {
	svc := &GenericService[*bug045Doc]{config: Config[*bug045Doc]{VersionMode: true}}
	err := svc.BatchUpdateByIDs(context.Background(), nil, map[string]any{"name": "x"})
	if !errors.Is(err, errs.ErrBatchUpdateSimpleNotSupportVersion) {
		t.Fatalf("version mode must still return ErrBatchUpdateSimpleNotSupportVersion, got %v", err)
	}
}

// TestBatchUpdateByIDsEmptyUpdatesUnchanged updates 空仍报「缺少必需参数: updates」。
func TestBatchUpdateByIDsEmptyUpdatesUnchanged(t *testing.T) {
	svc := &GenericService[*bug045Doc]{}
	if err := svc.BatchUpdateByIDs(context.Background(), []any{"id1"}, nil); !errs.IsMissingParam(err) {
		t.Fatalf("empty updates must still be missing-param error, got %v", err)
	}
}

// TestBatchUpdateByIDsNormalPath 非空 ids + 非空 updates 正常走 repo，并自动补审计字段。
func TestBatchUpdateByIDsNormalPath(t *testing.T) {
	repo := &bug052MockRepo{}
	svc := &GenericService[*bug045Doc]{repo: repo}

	if err := svc.BatchUpdateByIDs(context.Background(), []any{"id1", "id2"}, map[string]any{"name": "x"}); err != nil {
		t.Fatalf("normal path: %v", err)
	}
	if repo.callCount != 1 {
		t.Fatalf("repo.UpdateByIDs must be called once, got %d", repo.callCount)
	}
	if len(repo.lastIDs) != 2 {
		t.Errorf("ids mismatch: %v", repo.lastIDs)
	}
	if v, ok := repo.lastMap["name"]; !ok || v != "x" {
		t.Errorf("updates mismatch: %v", repo.lastMap)
	}
	if _, ok := repo.lastMap["updated_at"]; !ok {
		t.Errorf("updated_at must be auto-filled: %v", repo.lastMap)
	}
}
