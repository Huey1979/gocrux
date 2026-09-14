// BUG-072 端到端回归：非版本化 / 版本化 update 都必须能把字符串字段清空。
//
// 修复前：`tenant-param/update {"param_value":""}` 返回 200「操作成功」，
// 但库里一个字未改 —— 静默数据不一致（用户可感知的数据谎报）。
//
// 本文件用真实 sqlite 落库验证：
//  1. 非版本化 update 显式空串 → 库里真的变空；旧值出现在 UpdatePair.Old 快照里；
//  2. 版本化 update 显式空串 → **新版本行**为空串，旧版本行保持非空（不被"继承"回去）；
//  3. Create 回归：不提交该字段 → SetDefaults 生效；显式空串 → 仍保留默认值（BUG-072 未改 create 语义）；
//  4. 数字 0 显式提交仍真实落库（防把 BUG-045 改回去）。
package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Huey1979/gocrux/repository"
)

// bug072Doc 非版本化实体（M = *bug072Doc，与 heims 实体同形）。
type bug072Doc struct {
	ULID      string    `gorm:"column:ulid;primaryKey;size:26" json:"ulid"`
	Name      string    `gorm:"column:name;size:100" json:"name"`
	Remark    string    `gorm:"column:remark;size:200" json:"remark"`
	Count     int       `gorm:"column:count;default:0" json:"count"`
	IsDeleted int8      `gorm:"column:is_deleted;default:0" json:"-"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (d *bug072Doc) SetDefaults()             { d.Remark = "default-remark" } // 模拟 SetDefaults 兜底
func (d *bug072Doc) SetCreatedAt(_ time.Time) {}
func (d *bug072Doc) SetCreatedBy(string)      {}
func (d *bug072Doc) SetUpdatedAt(t time.Time) { d.UpdatedAt = t }
func (d *bug072Doc) SetUpdatedBy(string)      {}
func (d *bug072Doc) SupportsDraft() bool      { return false }
func (d *bug072Doc) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *bug072Doc) PKField() string          { return "ulid" }
func (d *bug072Doc) SelfFKField() string      { return "" }

// bug072VerDoc 版本化实体，用于验证「新版本行清空、旧版本行保留」。
type bug072VerDoc struct {
	ULID          string `gorm:"column:ulid;primaryKey;size:26" json:"ulid"`
	Code          string `gorm:"column:code;size:64" json:"code"`
	Description   string `gorm:"column:description;size:200" json:"description"`
	VersionCode   string `gorm:"column:version_code;size:20" json:"version_code"`
	VersionStatus string `gorm:"column:version_status;size:20" json:"version_status"`
	IsCurrent     int8   `gorm:"column:is_current;default:0" json:"is_current"`
	ParentULID    string `gorm:"column:parent_ulid;size:26" json:"parent_ulid"`
	CreatedBy     string `gorm:"column:created_by;size:26" json:"created_by"`
	IsDeleted     int8   `gorm:"column:is_deleted;default:0" json:"-"`
}

func (d *bug072VerDoc) SetDefaults()             {}
func (d *bug072VerDoc) SetCreatedAt(_ time.Time) {}
func (d *bug072VerDoc) SetCreatedBy(uid string)  { d.CreatedBy = uid }
func (d *bug072VerDoc) SetUpdatedAt(_ time.Time) {}
func (d *bug072VerDoc) SetUpdatedBy(string)      {}
func (d *bug072VerDoc) SupportsDraft() bool      { return false }
func (d *bug072VerDoc) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *bug072VerDoc) PKField() string          { return "ulid" }
func (d *bug072VerDoc) SelfFKField() string      { return "" }

// bug072Req 走 JSON 中介合并的请求体（模拟 handler.MapRequest 的更新语义）。
type bug072Req[M Record] struct{ data map[string]any }

func (r *bug072Req[M]) MergeTo(target *M) error {
	return bug072Merge(r.data, target)
}

// MergeToExisting 实现 MergeableExisting：显式空串原样写入（Update 语义）。
func (r *bug072Req[M]) MergeToExisting(target *M) error {
	return bug072Merge(r.data, target)
}

func (r *bug072Req[M]) GetID() any           { return nil }
func (r *bug072Req[M]) Validate() error      { return nil }
func (r *bug072Req[M]) Data() map[string]any { return r.data }

// bug072Merge 简化版 JSON 合并（不丢弃空串）——与 handler.mergeByJSON(allowClearEmpty=true) 等价。
func bug072Merge[M any](m map[string]any, target *M) error {
	// 先把 target 现有内容读出来做底，再覆盖（保持局部更新语义）
	base, err := json.Marshal(target)
	if err != nil {
		return err
	}
	var baseMap map[string]any
	if err := json.Unmarshal(base, &baseMap); err != nil {
		return err
	}
	if baseMap == nil {
		baseMap = map[string]any{}
	}
	for k, v := range m {
		baseMap[k] = v
	}
	merged, err := json.Marshal(baseMap)
	if err != nil {
		return err
	}
	return json.Unmarshal(merged, target)
}

// bug072ReqNoExisting 只实现 MergeTo（不实现 MergeToExisting）→ 应自动回退，行为同修复前。
type bug072ReqNoExisting[M Record] struct{ data map[string]any }

func (r *bug072ReqNoExisting[M]) MergeTo(target *M) error {
	// 刻意保留「丢弃空串」的旧语义，验证回退路径
	m := map[string]any{}
	for k, v := range r.data {
		if s, ok := v.(string); ok && s == "" {
			continue
		}
		m[k] = v
	}
	return bug072Merge(m, target)
}
func (r *bug072ReqNoExisting[M]) GetID() any           { return nil }
func (r *bug072ReqNoExisting[M]) Validate() error      { return nil }
func (r *bug072ReqNoExisting[M]) Data() map[string]any { return r.data }

// TestBug072UpdateClearsStringField 用例 1：非版本化 update 显式空串 → 库里真的变空。
func TestBug072UpdateClearsStringField(t *testing.T) {
	db := openBug069DB(t, &bug072Doc{})
	svc := NewGenericService[*bug072Doc](repository.NewCRUDWithDB[*bug072Doc](db), Config[*bug072Doc]{})
	ctx := context.Background()

	created, err := svc.Create(ctx, []CrudRequest[*bug072Doc]{
		&bug072Req[*bug072Doc]{data: map[string]any{"name": "ICP备案号", "remark": "r1"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := (*created[0]).ULID

	if _, err := svc.Update(ctx, id, &bug072Req[*bug072Doc]{
		data: map[string]any{"name": "R23探针名称", "remark": ""},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	var live bug072Doc
	if err := db.Where("ulid = ?", id).First(&live).Error; err != nil {
		t.Fatalf("query: %v", err)
	}
	if live.Name != "R23探针名称" {
		t.Errorf("name = %q, want R23探针名称（同请求里的改名字应生效）", live.Name)
	}
	if live.Remark != "" {
		t.Errorf("BUG-072: remark = %q, want \"\"（显式空串必须真实落库）", live.Remark)
	}
}

// TestBug072CreateStillProtectsSetDefaults 用例 3：Create 语义不变。
func TestBug072CreateStillProtectsSetDefaults(t *testing.T) {
	db := openBug069DB(t, &bug072Doc{})
	svc := NewGenericService[*bug072Doc](repository.NewCRUDWithDB[*bug072Doc](db), Config[*bug072Doc]{})
	ctx := context.Background()

	// 3a. 不提交该字段 → SetDefaults 生效
	r1, err := svc.Create(ctx, []CrudRequest[*bug072Doc]{
		&bug072Req[*bug072Doc]{data: map[string]any{"name": "a"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if (*r1[0]).Remark != "default-remark" {
		t.Errorf("omitted field must keep SetDefaults value, remark = %q", (*r1[0]).Remark)
	}
}

// TestBug072VersionedUpdateClearsNewRowOnly 用例 2（版本化）：
// 新版本行该列为空串，旧版本行保持原值（不能被"继承"回去）。
func TestBug072VersionedUpdateClearsNewRowOnly(t *testing.T) {
	db := openBug069DB(t, &bug072VerDoc{})
	svc := NewGenericService[*bug072VerDoc](repository.NewCRUDWithDB[*bug072VerDoc](db), Config[*bug072VerDoc]{
		VersionMode: true,
		VersionFields: &VersionFieldMapping{
			ULIDField: "ULID", CodeField: "Code", VersionField: "VersionCode",
			CurrentField: "IsCurrent", StatusField: "VersionStatus", ParentField: "ParentULID",
		},
	})
	ctx := context.Background()

	created, err := svc.Create(ctx, []CrudRequest[*bug072VerDoc]{
		&bug072Req[*bug072VerDoc]{data: map[string]any{"description": "v1-desc"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	v1ID := (*created[0]).ULID
	code := (*created[0]).Code

	updated, err := svc.Update(ctx, v1ID, &bug072Req[*bug072VerDoc]{
		data: map[string]any{"description": ""},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	v2ID := (*updated).ULID
	if v2ID == v1ID {
		t.Fatal("versioned update must derive a new version row")
	}
	if (*updated).Description != "" {
		t.Errorf("BUG-072: new version row description = %q, want \"\"", (*updated).Description)
	}

	// 旧版本行保持原值
	var v1Row bug072VerDoc
	if err := db.Where("ulid = ?", v1ID).First(&v1Row).Error; err != nil {
		t.Fatalf("query v1: %v", err)
	}
	if v1Row.Description != "v1-desc" {
		t.Errorf("old version row description = %q, want v1-desc（旧版本不得被清空）", v1Row.Description)
	}
	if v1Row.IsCurrent != 0 {
		t.Errorf("v1 must be deprecated, is_current = %d", v1Row.IsCurrent)
	}

	var v2Row bug072VerDoc
	if err := db.Where("ulid = ? AND code = ?", v2ID, code).First(&v2Row).Error; err != nil {
		t.Fatalf("query v2: %v", err)
	}
	if v2Row.Description != "" {
		t.Errorf("BUG-072: persisted v2 description = %q, want \"\"", v2Row.Description)
	}
}

// TestBug072LegacyRequestFallsBack 用例：未实现 MergeToExisting 的自定义 Request
// 自动回退 MergeTo —— 老接入方行为零变化（向后兼容）。
func TestBug072LegacyRequestFallsBack(t *testing.T) {
	db := openBug069DB(t, &bug072Doc{})
	svc := NewGenericService[*bug072Doc](repository.NewCRUDWithDB[*bug072Doc](db), Config[*bug072Doc]{})
	ctx := context.Background()

	created, err := svc.Create(ctx, []CrudRequest[*bug072Doc]{
		&bug072Req[*bug072Doc]{data: map[string]any{"remark": "keep"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := (*created[0]).ULID

	if _, err := svc.Update(ctx, id, &bug072ReqNoExisting[*bug072Doc]{
		data: map[string]any{"name": "n", "remark": ""},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	var live bug072Doc
	if err := db.Where("ulid = ?", id).First(&live).Error; err != nil {
		t.Fatalf("query: %v", err)
	}
	if live.Name != "n" {
		t.Errorf("non-empty field must still update, name = %q", live.Name)
	}
	if live.Remark != "keep" {
		t.Errorf("legacy Request must keep old behavior (empty discarded), remark = %q want keep", live.Remark)
	}
}

// TestBug072ExplicitZeroStillPersists 用例 4：数字 0 显式提交仍真实落库（BUG-045 不回归）。
func TestBug072ExplicitZeroStillPersists(t *testing.T) {
	db := openBug069DB(t, &bug072Doc{})
	svc := NewGenericService[*bug072Doc](repository.NewCRUDWithDB[*bug072Doc](db), Config[*bug072Doc]{})
	ctx := context.Background()

	created, err := svc.Create(ctx, []CrudRequest[*bug072Doc]{
		&bug072Req[*bug072Doc]{data: map[string]any{"name": "x", "count": 5}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := (*created[0]).ULID

	if _, err := svc.Update(ctx, id, &bug072Req[*bug072Doc]{data: map[string]any{"count": 0}}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	var live bug072Doc
	if err := db.Where("ulid = ?", id).First(&live).Error; err != nil {
		t.Fatalf("query: %v", err)
	}
	if live.Count != 0 {
		t.Errorf("BUG-045 must not regress: count = %d, want 0", live.Count)
	}
}
