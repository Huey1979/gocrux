// BUG-076 回归测试：版本化实体删除「非 published 的当前版本」后，
// is_current 无人回填，整条 code 族在配置列表里变幽灵。
//
// 缺陷场景（heims 实测）：一个已发布上线的版本化实体，
//
//	S1 新建并发布         → 列表 1 行「已发布 v1.0」
//	S2 编辑 → 存草稿      → 列表 1 行「草稿 v1.1」（此时 v1.0 已被让位成 is_current=0）
//	S3 页面删除这条草稿   → 列表 0 行 ← 幽灵态：全族 is_current 之和为 0
//
// 而运行时仍按 version_status='published' 跑着那条看不见的版本。
//
// 修复（方案 A）：删除后若族内已无 is_current=1，且存在
// `version_status=published AND is_deleted=0` 的行，则把其中最新的一条
// 只改 is_current=1（不动 status），把配置态交还给线上版本。
//
// 本文件覆盖：① 删草稿后 is_current 交还给 published 行（核心回归）；
// ② 删 published 当前版本仍整族下线（不回归，方案 B 的判据）；
// ③ 族内无 published 可交还时维持现状；④ 多 published 时取 published_at 最新；
// ⑤ 交还后按 is_current=1 过滤的读路径能重新看到该实体。
package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Huey1979/gocrux/repository"

	"gorm.io/gorm"
)

// bug076Doc 版本化实体（M = *bug076Doc），含 published_at 用于「取最新」判定。
type bug076Doc struct {
	ULID          string     `gorm:"column:ulid;primaryKey;size:26" json:"ulid"`
	Code          string     `gorm:"column:code;size:64" json:"code"`
	Name          string     `gorm:"column:name;size:100" json:"name"`
	VersionCode   string     `gorm:"column:version_code;size:20" json:"version_code"`
	VersionStatus string     `gorm:"column:version_status;size:20" json:"version_status"`
	IsCurrent     int8       `gorm:"column:is_current;default:0" json:"is_current"`
	ParentULID    string     `gorm:"column:parent_ulid;size:26" json:"parent_ulid"`
	PublishedAt   *time.Time `gorm:"column:published_at" json:"published_at"`
	IsDeleted     int8       `gorm:"column:is_deleted;default:0" json:"-"`
}

func (d *bug076Doc) SetDefaults()             {}
func (d *bug076Doc) SetCreatedAt(_ time.Time) {}
func (d *bug076Doc) SetCreatedBy(string)      {}
func (d *bug076Doc) SetUpdatedAt(_ time.Time) {}
func (d *bug076Doc) SetUpdatedBy(string)      {}
func (d *bug076Doc) SupportsDraft() bool      { return true }
func (d *bug076Doc) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *bug076Doc) PKField() string          { return "ulid" }
func (d *bug076Doc) SelfFKField() string      { return "" }

type bug076Req[M Record] struct{ data map[string]any }

func (r *bug076Req[M]) MergeTo(target *M) error {
	return bug076Merge(r.data, target)
}
func (r *bug076Req[M]) MergeToExisting(target *M) error {
	return bug076Merge(r.data, target)
}
func (r *bug076Req[M]) GetID() any           { return nil }
func (r *bug076Req[M]) Validate() error      { return nil }
func (r *bug076Req[M]) Data() map[string]any { return r.data }

func bug076Merge[M any](m map[string]any, target *M) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, target)
}

// newBug076Svc 返回服务与底层 *gorm.DB（用于直接查库断言）。
func newBug076Svc(t *testing.T) (*GenericService[*bug076Doc], *gorm.DB) {
	t.Helper()
	db := openBug069DB(t, &bug076Doc{})
	svc := NewGenericService[*bug076Doc](repository.NewCRUDWithDB[*bug076Doc](db), Config[*bug076Doc]{
		EntityName:  "bug076_doc",
		VersionMode: true,
		VersionFields: &VersionFieldMapping{
			ULIDField: "ULID", CodeField: "Code", VersionField: "VersionCode",
			CurrentField: "IsCurrent", StatusField: "VersionStatus",
			ParentField: "ParentULID", PublishedAtField: "PublishedAt",
		},
	})
	return svc, db
}

// seedBug076Published 直接落一行 published 版本（模拟「已上线」的现状）。
func seedBug076Published(t *testing.T, svc *GenericService[*bug076Doc], _ any, ulid, code, name string, isCurrent int8, publishedAt time.Time) {
	t.Helper()
	row := &bug076Doc{
		ULID: ulid, Code: code, Name: name,
		VersionCode: "v1.0", VersionStatus: string(VersionStatusPublished),
		IsCurrent: isCurrent, PublishedAt: &publishedAt,
	}
	if err := svc.repo.Insert(context.Background(), &row); err != nil {
		t.Fatalf("seed published: %v", err)
	}
}

// seedBug076Draft 落一行草稿版本（is_current=1）。
func seedBug076Draft(t *testing.T, svc *GenericService[*bug076Doc], ulid, code, name string) {
	t.Helper()
	row := &bug076Doc{
		ULID: ulid, Code: code, Name: name,
		VersionCode: "v1.1", VersionStatus: string(VersionStatusDraft),
		IsCurrent: 1,
	}
	if err := svc.repo.Insert(context.Background(), &row); err != nil {
		t.Fatalf("seed draft: %v", err)
	}
}

// TestBug076DeleteDraftHandsBackCurrent 用例 ①（核心回归）：
// 删掉草稿后，published 行必须重新拿到 is_current=1（配置列表恢复可见）。
func TestBug076DeleteDraftHandsBackCurrent(t *testing.T) {
	svc, _ := newBug076Svc(t)
	ctx := context.Background()
	pubTime := time.Now().Add(-time.Hour)

	seedBug076Published(t, svc, nil, "pub0001", "code_ghost", "线上版本", 0, pubTime)
	seedBug076Draft(t, svc, "draft001", "code_ghost", "草稿")

	// 删除草稿（版本化 delete = 废弃）
	if err := svc.Delete(ctx, []any{"draft001"}, nil); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// published 行必须重新成为当前版本
	var pub bug076Doc
	if err := svc.CRUDRepo().DB(ctx).Where("ulid = ?", "pub0001").First(&pub).Error; err != nil {
		t.Fatalf("query published: %v", err)
	}
	if pub.IsCurrent != 1 {
		t.Errorf("BUG-076: published row must regain is_current=1, got %d（幽灵态未修复）", pub.IsCurrent)
	}
	if pub.VersionStatus != string(VersionStatusPublished) {
		t.Errorf("BUG-076: handback must NOT touch version_status, got %q", pub.VersionStatus)
	}

	// 草稿行仍是被废弃状态
	var draft bug076Doc
	if err := svc.CRUDRepo().DB(ctx).Where("ulid = ?", "draft001").First(&draft).Error; err != nil {
		t.Fatalf("query draft: %v", err)
	}
	if draft.IsCurrent != 0 {
		t.Errorf("draft must stay deprecated, is_current = %d", draft.IsCurrent)
	}
	if draft.VersionStatus != string(VersionStatusDeprecated) {
		t.Errorf("draft version_status = %q, want deprecated", draft.VersionStatus)
	}
}

// TestBug076DeletePublishedStillTakesWholeFamilyOffline 用例 ②：
// 删 published 当前版本仍整族下线（方案 B 的判据 —— 此处行为不变）。
func TestBug076DeletePublishedStillTakesWholeFamilyOffline(t *testing.T) {
	svc, _ := newBug076Svc(t)
	ctx := context.Background()

	seedBug076Published(t, svc, nil, "pub0002", "code_offline", "线上版本", 1, time.Now().Add(-time.Hour))

	if err := svc.Delete(ctx, []any{"pub0002"}, nil); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	var cnt int64
	if err := svc.CRUDRepo().DB(ctx).Model(&bug076Doc{}).
		Where("code = ? AND is_current = 1", "code_offline").Count(&cnt).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt != 0 {
		t.Errorf("BUG-076: deleting the published current version must take the family offline, current rows = %d", cnt)
	}
}

// TestBug076NoPublishedToHandBack 用例 ③：
// 族内只有草稿（从没发布过）→ 删草稿后不应凭空产生 is_current=1。
func TestBug076NoPublishedToHandBack(t *testing.T) {
	svc, _ := newBug076Svc(t)
	ctx := context.Background()

	seedBug076Draft(t, svc, "draft002", "code_never_pub", "草稿")

	if err := svc.Delete(ctx, []any{"draft002"}, nil); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	var cnt int64
	if err := svc.CRUDRepo().DB(ctx).Model(&bug076Doc{}).
		Where("code = ? AND is_current = 1", "code_never_pub").Count(&cnt).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt != 0 {
		t.Errorf("no published row exists ⇒ nothing to hand back, current rows = %d", cnt)
	}
}

// TestBug076HandBackPicksLatestPublished 用例 ④：
// 族内有多条 published 时交还 published_at 最新的一条。
func TestBug076HandBackPicksLatestPublished(t *testing.T) {
	svc, _ := newBug076Svc(t)
	ctx := context.Background()
	base := time.Now().Add(-24 * time.Hour)

	seedBug076Published(t, svc, nil, "old00001", "code_multi", "老线上", 0, base)
	seedBug076Published(t, svc, nil, "new00001", "code_multi", "新线上", 0, base.Add(2*time.Hour))
	seedBug076Draft(t, svc, "draft003", "code_multi", "草稿")

	if err := svc.Delete(ctx, []any{"draft003"}, nil); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	var newest bug076Doc
	if err := svc.CRUDRepo().DB(ctx).Where("ulid = ?", "new00001").First(&newest).Error; err != nil {
		t.Fatalf("query newest: %v", err)
	}
	if newest.IsCurrent != 1 {
		t.Errorf("BUG-076: latest published must get is_current=1, got %d", newest.IsCurrent)
	}

	var older bug076Doc
	if err := svc.CRUDRepo().DB(ctx).Where("ulid = ?", "old00001").First(&older).Error; err != nil {
		t.Fatalf("query older: %v", err)
	}
	if older.IsCurrent != 0 {
		t.Errorf("older published must stay is_current=0, got %d", older.IsCurrent)
	}
}

// TestBug076AfterHandbackListSeesEntity 用例 ⑤：
// 交还后，按 is_current=1 过滤的配置态读路径能重新看到该实体（幽灵态消失）。
func TestBug076AfterHandbackListSeesEntity(t *testing.T) {
	svc, db := newBug076Svc(t)
	ctx := context.Background()

	seedBug076Published(t, svc, nil, "pub0003", "code_visible", "线上版本", 0, time.Now().Add(-time.Hour))
	seedBug076Draft(t, svc, "draft004", "code_visible", "草稿")

	// 配置列表的可见性判据 = is_current=1（服务层 List 还会叠加草稿可见性，
	// 这里直接按同一判据查库，聚焦 BUG-076 本身）。
	countCurrent := func() []bug076Doc {
		t.Helper()
		var rows []bug076Doc
		if err := db.Where("code = ? AND is_current = 1", "code_visible").Find(&rows).Error; err != nil {
			t.Fatalf("query current rows: %v", err)
		}
		return rows
	}

	// 删除前：草稿是当前版本
	before := countCurrent()
	if len(before) != 1 || before[0].ULID != "draft004" {
		t.Fatalf("before delete: expect the draft to be the current version, got %+v", before)
	}

	if err := svc.Delete(ctx, []any{"draft004"}, nil); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// 删除后：必须仍有当前版本（修复前为空 = 幽灵态）
	after := countCurrent()
	if len(after) != 1 {
		t.Fatalf("BUG-076: entity must stay visible after deleting its draft, got %d current rows（幽灵态）", len(after))
	}
	if after[0].ULID != "pub0003" {
		t.Errorf("current row = %s, want the published row pub0003", after[0].ULID)
	}
	if after[0].VersionStatus != string(VersionStatusPublished) {
		t.Errorf("current row status = %q, want published", after[0].VersionStatus)
	}
}
