package service

import (
	"context"
	"testing"

	"github.com/Huey1979/gocrux/repository"
)

// ============================================================
// BUG-070 回归测试：引用展开（References / ChildRefs）不能用「当前有效」语义
//
// 背景：批量引用展开复用 service.List → _doList，后者会追加
// 软删过滤（非版本化）或 is_current=1 + 版本可见性过滤（版本化），
// 导致引用目标一旦被软删或是历史版本，锚点就被静默丢弃；
// 而单条 get 走 DoGetByID（不过滤软删）却能返回 —— 同一份数据两种结果。
//
// 修复：_doList 支持「引用解析模式」（ctx 标记 WithResolveMode），
// 该模式不追加上述默认过滤；向下级联（Cascades）不注入该标记，行为不变。
// ============================================================

// TestBug070ResolveModeKeepsDeletedTarget 非版本化引用目标：
// 软删后普通 list 仍过滤（不回归），引用解析模式应保留锚点并携带 is_deleted=1。
func TestBug070ResolveModeKeepsDeletedTarget(t *testing.T) {
	db := openBug069DB(t, &bug069Doc{})
	svc := NewGenericService[*bug069Doc](repository.NewCRUDWithDB[*bug069Doc](db), Config[*bug069Doc]{})
	ctx := context.Background()

	created, err := svc.Create(ctx, []CrudRequest[*bug069Doc]{
		&bug069Req[*bug069Doc]{data: map[string]any{"name": "live"}},
		&bug069Req[*bug069Doc]{data: map[string]any{"name": "deleted"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	liveID, delID := (*created[0]).ULID, (*created[1]).ULID
	if err := svc.Delete(ctx, []any{delID}, nil); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	idsQuery := func() repository.ListFilters {
		return repository.ListFilters{
			Filters:  []repository.Filter{{Field: "ulid", Op: repository.OpIn, Value: []any{liveID, delID}}},
			Page:     1,
			PageSize: 0,
		}
	}

	// 普通列表：只返回未删（BUG-069 之后的行为，不回归）
	normal, _, err := svc.List(ctx, idsQuery())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(normal) != 1 {
		t.Fatalf("normal list must exclude deleted, got %d records", len(normal))
	}
	if (*normal[0]).ULID != liveID {
		t.Errorf("normal list[0] = %s, want live %s", (*normal[0]).ULID, liveID)
	}

	// BUG-070 核心：引用解析模式两个锚点都要返回
	resolved, _, err := svc.List(WithResolveMode(ctx), idsQuery())
	if err != nil {
		t.Fatalf("List(resolve mode): %v", err)
	}
	if len(resolved) != 2 {
		t.Fatalf("BUG-070: resolve mode must keep both anchors, got %d", len(resolved))
	}
	foundDeleted := false
	for i := range resolved {
		if (*resolved[i]).ULID == delID {
			foundDeleted = true
			if (*resolved[i]).IsDeleted != 1 {
				t.Errorf("deleted anchor must carry is_deleted=1, got %d", (*resolved[i]).IsDeleted)
			}
			if (*resolved[i]).Name != "deleted" {
				t.Errorf("deleted anchor name = %q, want deleted", (*resolved[i]).Name)
			}
		}
	}
	if !foundDeleted {
		t.Error("BUG-070: deleted target missing from resolve-mode result")
	}
}

// TestBug070ResolveModeKeepsHistoryVersion 版本化引用目标：
// 引用历史版本 ULID 时不得被 is_current=1 过滤掉，也不得被隐式替换为当前版本。
func TestBug070ResolveModeKeepsHistoryVersion(t *testing.T) {
	db := openBug069DB(t, &bug069VerDoc{})
	svc := NewGenericService[*bug069VerDoc](repository.NewCRUDWithDB[*bug069VerDoc](db), Config[*bug069VerDoc]{
		VersionMode: true,
		VersionFields: &VersionFieldMapping{
			ULIDField: "ULID", CodeField: "Code", VersionField: "VersionCode",
			CurrentField: "IsCurrent", StatusField: "VersionStatus", ParentField: "ParentULID",
		},
	})
	ctx := context.Background()

	created, err := svc.Create(ctx, []CrudRequest[*bug069VerDoc]{
		&bug069Req[*bug069VerDoc]{data: map[string]any{"name": "v1"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	v1ID, code := (*created[0]).ULID, (*created[0]).Code

	// 生成 v2：v1 退位（is_current=0）
	updated, err := svc.Update(ctx, v1ID, &bug069Req[*bug069VerDoc]{data: map[string]any{"name": "v2"}})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	v2ID := (*updated).ULID

	idsQuery := func() repository.ListFilters {
		return repository.ListFilters{
			Filters:  []repository.Filter{{Field: "ulid", Op: repository.OpIn, Value: []any{v1ID, v2ID}}},
			Page:     1,
			PageSize: 0,
		}
	}

	// 普通列表：只返回当前版本 v2（不回归）
	normal, _, err := svc.List(ctx, idsQuery())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(normal) != 1 || (*normal[0]).ULID != v2ID {
		t.Fatalf("normal list must return only current version v2, got %d records", len(normal))
	}

	// BUG-070 核心：引用解析模式必须能拿到 v1 历史版本行
	resolved, _, err := svc.List(WithResolveMode(ctx), idsQuery())
	if err != nil {
		t.Fatalf("List(resolve mode): %v", err)
	}
	if len(resolved) != 2 {
		t.Fatalf("BUG-070: resolve mode must keep history version, got %d records", len(resolved))
	}
	var v1 *bug069VerDoc
	for i := range resolved {
		if (*resolved[i]).ULID == v1ID {
			v1 = resolved[i]
		}
	}
	if v1 == nil {
		t.Fatal("BUG-070: history version v1 missing from resolve-mode result")
	}
	if v1.Name != "v1" {
		t.Errorf("history version must be the exact referenced row, name = %q want v1", v1.Name)
	}
	if v1.IsCurrent != 0 {
		t.Errorf("history version must keep is_current=0, got %d", v1.IsCurrent)
	}
	if v1.Code != code {
		t.Errorf("history version code = %q, want %q", v1.Code, code)
	}
}

// TestBug070ResolveModeStillGuardsDraft BUG-070 复核（安全点）：
// 引用解析模式放开软删 / is_current / published 过滤，但**不放开草稿可见性** ——
// 未发布草稿不能因为被某条记录引用就对外暴露。
//
//   - 未登录：草稿锚点不可解析（但 deprecated 历史版本必须仍可解析，见 TestBug070ResolveModeKeepsHistoryVersion）
//   - 创建者本人登录：草稿锚点可解析
//   - 其他登录用户：草稿锚点不可解析
func TestBug070ResolveModeStillGuardsDraft(t *testing.T) {
	db := openBug069DB(t, &bug069VerDoc{})
	svc := NewGenericService[*bug069VerDoc](repository.NewCRUDWithDB[*bug069VerDoc](db), Config[*bug069VerDoc]{
		VersionMode: true,
		VersionFields: &VersionFieldMapping{
			ULIDField: "ULID", CodeField: "Code", VersionField: "VersionCode",
			CurrentField: "IsCurrent", StatusField: "VersionStatus", ParentField: "ParentULID",
		},
	})

	authorCtx := context.WithValue(context.Background(), CtxKeyUserULID, "author-ulid")
	created, err := svc.Create(authorCtx, []CrudRequest[*bug069VerDoc]{
		&bug069Req[*bug069VerDoc]{data: map[string]any{"name": "draft-row"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	draftID := (*created[0]).ULID
	// Create 默认 published → 手工置为草稿态
	if err := db.Model(&bug069VerDoc{}).Where("ulid = ?", draftID).
		Update("version_status", string(VersionStatusDraft)).Error; err != nil {
		t.Fatalf("set draft: %v", err)
	}

	q := func() repository.ListFilters {
		return repository.ListFilters{
			Filters:  []repository.Filter{{Field: "ulid", Op: repository.OpIn, Value: []any{draftID}}},
			Page:     1,
			PageSize: 0,
		}
	}

	// 1) 未登录：草稿不可见
	anon := context.Background()
	normal, _, err := svc.List(anon, q())
	if err != nil {
		t.Fatalf("List(anon): %v", err)
	}
	if len(normal) != 0 {
		t.Fatalf("anonymous list must hide draft, got %d records", len(normal))
	}
	resolved, _, err := svc.List(WithResolveMode(anon), q())
	if err != nil {
		t.Fatalf("List(resolve, anon): %v", err)
	}
	if len(resolved) != 0 {
		t.Errorf("BUG-070 复核：resolve mode must NOT expose draft to anonymous caller, got %d", len(resolved))
	}

	// 2) 其他登录用户：草稿不可见
	otherCtx := context.WithValue(context.Background(), CtxKeyUserULID, "other-ulid")
	resolved, _, err = svc.List(WithResolveMode(otherCtx), q())
	if err != nil {
		t.Fatalf("List(resolve, other): %v", err)
	}
	if len(resolved) != 0 {
		t.Errorf("BUG-070 复核：resolve mode must NOT expose others' draft, got %d", len(resolved))
	}

	// 3) 创建者本人：草稿锚点可解析，且状态字段保留
	resolved, _, err = svc.List(WithResolveMode(authorCtx), q())
	if err != nil {
		t.Fatalf("List(resolve, author): %v", err)
	}
	if len(resolved) != 1 {
		t.Fatalf("author must resolve own draft anchor, got %d", len(resolved))
	}
	if (*resolved[0]).VersionStatus != string(VersionStatusDraft) {
		t.Errorf("draft anchor must carry version_status=draft, got %q", (*resolved[0]).VersionStatus)
	}
}
