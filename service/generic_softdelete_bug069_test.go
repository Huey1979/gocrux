package service

import (
	"context"
	"encoding/json"
	goerrors "errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	errs "github.com/Huey1979/gocrux/errors"
	"github.com/Huey1979/gocrux/internal/model/entity"
	"github.com/Huey1979/gocrux/repository"
)

// ============================================================
// BUG-069 回归测试（一）：sqlite 集成级
//
// 现象：软删除生效（is_deleted=1、list 已排除），但按主键的 get / update
// 完全没有软删过滤 —— 已删记录可被精确读回、可被改写；版本化实体甚至会以
// 已删旧行为底插入 is_current=1 的新行（"复活"）。
//
// 修复：服务层收口（_doGet / _beforeUpdate / BatchUpdateByIDs + deletedCol helper）。
// 覆盖：非版本化、版本化、无软删列实体、自定义 DeletedField/DeletedValue 配置。
// ============================================================

// bug069Doc 非版本化测试实体（对应 heims SysExternalApp：IsDeleted int8 + 主键 ulid）。
type bug069Doc struct {
	ULID      string    `gorm:"column:ulid;primaryKey;size:26" json:"ulid"`
	Name      string    `gorm:"column:name;size:100" json:"name"`
	IsDeleted int8      `gorm:"column:is_deleted;default:0" json:"-"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updated_at"`
	UpdatedBy string    `gorm:"column:updated_by;size:26" json:"updated_by"` // Restore/BatchUpdateByIDs 自动补审计字段
}

func (d *bug069Doc) SetDefaults()             {}
func (d *bug069Doc) SetCreatedAt(_ time.Time) {}
func (d *bug069Doc) SetCreatedBy(string)      {}
func (d *bug069Doc) SetUpdatedAt(t time.Time) { d.UpdatedAt = t }
func (d *bug069Doc) SetUpdatedBy(uid string)  { d.UpdatedBy = uid }
func (d *bug069Doc) SupportsDraft() bool      { return false }
func (d *bug069Doc) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *bug069Doc) PKField() string          { return "ulid" }
func (d *bug069Doc) SelfFKField() string      { return "" }

// bug069NoDelDoc 无软删列实体（SetDelete=false）—— 不能被误加过滤条件。
type bug069NoDelDoc struct {
	ULID string `gorm:"column:ulid;primaryKey;size:26" json:"ulid"`
	Name string `gorm:"column:name;size:100" json:"name"`
}

func (d *bug069NoDelDoc) SetDefaults()             {}
func (d *bug069NoDelDoc) SetCreatedAt(_ time.Time) {}
func (d *bug069NoDelDoc) SetCreatedBy(string)      {}
func (d *bug069NoDelDoc) SetUpdatedAt(_ time.Time) {}
func (d *bug069NoDelDoc) SetUpdatedBy(string)      {}
func (d *bug069NoDelDoc) SupportsDraft() bool      { return false }
func (d *bug069NoDelDoc) SetDelete() bool          { return false }
func (d *bug069NoDelDoc) PKField() string          { return "ulid" }
func (d *bug069NoDelDoc) SelfFKField() string      { return "" }

// bug069CustomDoc 自定义软删列实体：列名 deleted，未删值 "n"、已删值 "y"。
type bug069CustomDoc struct {
	ULID    string `gorm:"column:ulid;primaryKey;size:26" json:"ulid"`
	Name    string `gorm:"column:name;size:100" json:"name"`
	Deleted string `gorm:"column:deleted;size:4" json:"-"`
}

func (d *bug069CustomDoc) SetDefaults()             { d.Deleted = "n" }
func (d *bug069CustomDoc) SetCreatedAt(_ time.Time) {}
func (d *bug069CustomDoc) SetCreatedBy(string)      {}
func (d *bug069CustomDoc) SetUpdatedAt(_ time.Time) {}
func (d *bug069CustomDoc) SetUpdatedBy(string)      {}
func (d *bug069CustomDoc) SupportsDraft() bool      { return false }
func (d *bug069CustomDoc) SetDelete() bool          { d.Deleted = "y"; return true }
func (d *bug069CustomDoc) PKField() string          { return "ulid" }
func (d *bug069CustomDoc) SelfFKField() string      { return "" }

// bug069VerDoc 版本化测试实体。
type bug069VerDoc struct {
	ULID          string `gorm:"column:ulid;primaryKey;size:26" json:"ulid"`
	Code          string `gorm:"column:code;size:64" json:"code"`
	Name          string `gorm:"column:name;size:100" json:"name"`
	VersionCode   string `gorm:"column:version_code;size:20" json:"version_code"`
	VersionStatus string `gorm:"column:version_status;size:20" json:"version_status"`
	IsCurrent     int8   `gorm:"column:is_current;default:0" json:"is_current"`
	ParentULID    string `gorm:"column:parent_ulid;size:26" json:"parent_ulid"`
	IsDeleted     int8   `gorm:"column:is_deleted;default:0" json:"-"`
}

func (d *bug069VerDoc) SetDefaults()             {}
func (d *bug069VerDoc) SetCreatedAt(_ time.Time) {}
func (d *bug069VerDoc) SetCreatedBy(string)      {}
func (d *bug069VerDoc) SetUpdatedAt(_ time.Time) {}
func (d *bug069VerDoc) SetUpdatedBy(string)      {}
func (d *bug069VerDoc) SupportsDraft() bool      { return false }
func (d *bug069VerDoc) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *bug069VerDoc) PKField() string          { return "ulid" }
func (d *bug069VerDoc) SelfFKField() string      { return "" }

// bug069Req 泛型请求体（JSON 中介合并），可复用于上述各实体。
type bug069Req[M Record] struct{ data map[string]any }

func (r *bug069Req[M]) MergeTo(target *M) error {
	if len(r.data) == 0 {
		return nil
	}
	b, err := json.Marshal(r.data)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, *target)
}
func (r *bug069Req[M]) GetID() any           { return nil }
func (r *bug069Req[M]) Validate() error      { return nil }
func (r *bug069Req[M]) Data() map[string]any { return r.data }

// openBug069DB 打开独立 sqlite 内存库（每次全新库，避免测试间共享数据）。
func openBug069DB(t *testing.T, objs ...any) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sqlDB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(objs...); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return db
}

// TestBug069GetKeepsDeletedRecordForHook 用例 1（读路径语义）：
// create → delete → get **照常返回**记录（带 is_deleted=1）——
// 是否可见交由应用端 AfterGet 钩子判定，框架不拦截（回收站/恢复场景需要读得到）。
func TestBug069GetKeepsDeletedRecordForHook(t *testing.T) {
	db := openBug069DB(t, &bug069Doc{})
	svc := NewGenericService[*bug069Doc](repository.NewCRUDWithDB[*bug069Doc](db), Config[*bug069Doc]{})
	ctx := context.Background()

	created, err := svc.Create(ctx, []CrudRequest[*bug069Doc]{
		&bug069Req[*bug069Doc]{data: map[string]any{"name": "alive"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := (*created[0]).ULID

	// 删除前可读（不回归）
	got, err := svc.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get before delete: %v", err)
	}
	if (*got).Name != "alive" {
		t.Fatalf("name = %q, want alive", (*got).Name)
	}

	if err := svc.Delete(ctx, []any{id}, nil); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// 确认软删是置标记（行仍在、list 才排除）
	var raw bug069Doc
	if err := db.Where("ulid = ?", id).First(&raw).Error; err != nil {
		t.Fatalf("query raw row: %v", err)
	}
	if raw.IsDeleted != 1 {
		t.Fatalf("is_deleted = %d, want 1", raw.IsDeleted)
	}

	// BUG-069：读路径不过滤 —— 记录照常返回，且携带 is_deleted 标记供应用端判定
	gotAfterDelete, err := svc.Get(ctx, id)
	if err != nil {
		t.Fatalf("BUG-069: get must not filter deleted records (app layer decides), got %v", err)
	}
	if (*gotAfterDelete).IsDeleted != 1 {
		t.Errorf("deleted record must carry is_deleted=1 for app-layer judgement, got %d", (*gotAfterDelete).IsDeleted)
	}
	if !svc.IsSoftDeleted(gotAfterDelete) {
		t.Error("IsSoftDeleted must detect the returned record as deleted (app-layer hook entry point)")
	}
}

// TestBug069UpdateDeletedRecordRejected 用例 2：
// create → delete → update 必须失败且不落库（修复前 200 且字段被改写）。
func TestBug069UpdateDeletedRecordRejected(t *testing.T) {
	db := openBug069DB(t, &bug069Doc{})
	svc := NewGenericService[*bug069Doc](repository.NewCRUDWithDB[*bug069Doc](db), Config[*bug069Doc]{})
	ctx := context.Background()

	created, err := svc.Create(ctx, []CrudRequest[*bug069Doc]{
		&bug069Req[*bug069Doc]{data: map[string]any{"name": "before"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := (*created[0]).ULID
	if err := svc.Delete(ctx, []any{id}, nil); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := svc.Update(ctx, id, &bug069Req[*bug069Doc]{
		data: map[string]any{"name": "hacked"},
	}); !goerrors.Is(err, errs.ErrRecordNotFound) {
		t.Fatalf("BUG-069: Update deleted record must be ErrRecordNotFound, got %v", err)
	}

	var raw bug069Doc
	if err := db.Where("ulid = ?", id).First(&raw).Error; err != nil {
		t.Fatalf("query raw row: %v", err)
	}
	if raw.Name != "before" {
		t.Errorf("BUG-069: deleted record must not be modified, name = %q want before", raw.Name)
	}
}

// TestBug069ListStillExcludesDeleted 用例 3（不回归）：list 仍排除已删记录。
func TestBug069ListStillExcludesDeleted(t *testing.T) {
	db := openBug069DB(t, &bug069Doc{})
	svc := NewGenericService[*bug069Doc](repository.NewCRUDWithDB[*bug069Doc](db), Config[*bug069Doc]{})
	ctx := context.Background()

	created, err := svc.Create(ctx, []CrudRequest[*bug069Doc]{
		&bug069Req[*bug069Doc]{data: map[string]any{"name": "keep"}},
		&bug069Req[*bug069Doc]{data: map[string]any{"name": "drop"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	dropID := (*created[1]).ULID
	if err := svc.Delete(ctx, []any{dropID}, nil); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	list, _, err := svc.List(ctx, repository.ListFilters{Page: 1, PageSize: 0})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List len = %d, want 1", len(list))
	}
	if (*list[0]).Name != "keep" {
		t.Errorf("List[0].name = %q, want keep", (*list[0]).Name)
	}
}

// TestBug069VersionedDeletedRecordNotRevived 用例 4（版本化，后果最严重）：
// 已软删的版本行被 update 时，不得派生 is_current=1 的新版本行（"复活"）。
//
// 注：版本化 Delete 走 BatchDeprecateVersions（is_current=0 / status=deprecated），
// 不置 is_deleted；因此这里手工置 is_deleted=1 来构造"已软删的版本行"。
func TestBug069VersionedDeletedRecordNotRevived(t *testing.T) {
	db := openBug069DB(t, &bug069VerDoc{})
	svc := NewGenericService[*bug069VerDoc](repository.NewCRUDWithDB[*bug069VerDoc](db), Config[*bug069VerDoc]{
		VersionMode: true,
		VersionFields: &VersionFieldMapping{
			ULIDField:    "ULID",
			CodeField:    "Code",
			VersionField: "VersionCode",
			CurrentField: "IsCurrent",
			StatusField:  "VersionStatus",
			ParentField:  "ParentULID",
		},
	})
	ctx := context.Background()

	created, err := svc.Create(ctx, []CrudRequest[*bug069VerDoc]{
		&bug069Req[*bug069VerDoc]{data: map[string]any{"name": "v1"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id, code := (*created[0]).ULID, (*created[0]).Code

	// 构造"已软删"状态
	if err := db.Model(&bug069VerDoc{}).Where("ulid = ?", id).Update("is_deleted", int8(1)).Error; err != nil {
		t.Fatalf("mark deleted: %v", err)
	}

	if _, err := svc.Update(ctx, id, &bug069Req[*bug069VerDoc]{
		data: map[string]any{"name": "revived"},
	}); !goerrors.Is(err, errs.ErrRecordNotFound) {
		t.Fatalf("BUG-069: Update deleted versioned record must be ErrRecordNotFound, got %v", err)
	}

	var cnt int64
	if err := db.Model(&bug069VerDoc{}).Where("code = ?", code).Count(&cnt).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if cnt != 1 {
		t.Errorf("BUG-069: must not derive a new version row, rows = %d want 1", cnt)
	}
	var raw bug069VerDoc
	if err := db.Where("ulid = ?", id).First(&raw).Error; err != nil {
		t.Fatalf("query raw: %v", err)
	}
	if raw.Name != "v1" {
		t.Errorf("BUG-069: deleted versioned record must not be modified, name = %q want v1", raw.Name)
	}
}

// TestBug069NoSoftDeleteColumnEntityUnchanged 用例 5：
// SetDelete()==false（无软删列）的实体行为完全不变 —— get/update 正常，不误加过滤条件。
func TestBug069NoSoftDeleteColumnEntityUnchanged(t *testing.T) {
	db := openBug069DB(t, &bug069NoDelDoc{})
	svc := NewGenericService[*bug069NoDelDoc](repository.NewCRUDWithDB[*bug069NoDelDoc](db), Config[*bug069NoDelDoc]{})
	ctx := context.Background()

	created, err := svc.Create(ctx, []CrudRequest[*bug069NoDelDoc]{
		&bug069Req[*bug069NoDelDoc]{data: map[string]any{"name": "a"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := (*created[0]).ULID

	got, err := svc.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get (no soft-delete column entity) must work: %v", err)
	}
	if (*got).Name != "a" {
		t.Fatalf("name = %q, want a", (*got).Name)
	}
	updated, err := svc.Update(ctx, id, &bug069Req[*bug069NoDelDoc]{data: map[string]any{"name": "b"}})
	if err != nil {
		t.Fatalf("Update (no soft-delete column entity) must work: %v", err)
	}
	if (*updated).Name != "b" {
		t.Errorf("updated name = %q, want b", (*updated).Name)
	}
}

// TestBug069CustomDeletedFieldConfig 用例 6：
// 自定义 DeletedField/DeletedValue（deleted / "n"）在 get/update 路径同样生效。
func TestBug069CustomDeletedFieldConfig(t *testing.T) {
	db := openBug069DB(t, &bug069CustomDoc{})
	svc := NewGenericService[*bug069CustomDoc](repository.NewCRUDWithDB[*bug069CustomDoc](db), Config[*bug069CustomDoc]{
		DeletedField: "deleted",
		DeletedValue: "n",
	})
	ctx := context.Background()

	created, err := svc.Create(ctx, []CrudRequest[*bug069CustomDoc]{
		&bug069Req[*bug069CustomDoc]{data: map[string]any{"name": "alive"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := (*created[0]).ULID

	// 未删（deleted="n"）→ 正常读
	if _, err := svc.Get(ctx, id); err != nil {
		t.Fatalf("Get live record with custom deleted field: %v", err)
	}

	if err := db.Model(&bug069CustomDoc{}).Where("ulid = ?", id).Update("deleted", "y").Error; err != nil {
		t.Fatalf("mark deleted: %v", err)
	}
	// 读路径不过滤：记录照常返回，应用端用 IsSoftDeleted 判定可见性
	gotDeleted, err := svc.Get(ctx, id)
	if err != nil {
		t.Fatalf("get must not filter deleted records, got %v", err)
	}
	if !svc.IsSoftDeleted(gotDeleted) {
		t.Error("custom deleted field must be detected by IsSoftDeleted")
	}
	// 写路径收口：已删记录不可 update（需先 Restore）
	if _, err := svc.Update(ctx, id, &bug069Req[*bug069CustomDoc]{
		data: map[string]any{"name": "hacked"},
	}); !goerrors.Is(err, errs.ErrRecordNotFound) {
		t.Errorf("BUG-069: custom deleted field must be honored in update, got %v", err)
	}
}

// TestBug069BatchUpdateByIDsSkipsDeleted 用例 7：
// 按 id 集合的批量写剔除已删记录（修复前会静默改写已删行）。
func TestBug069BatchUpdateByIDsSkipsDeleted(t *testing.T) {
	db := openBug069DB(t, &bug069Doc{})
	svc := NewGenericService[*bug069Doc](repository.NewCRUDWithDB[*bug069Doc](db), Config[*bug069Doc]{})
	ctx := context.Background()

	created, err := svc.Create(ctx, []CrudRequest[*bug069Doc]{
		&bug069Req[*bug069Doc]{data: map[string]any{"name": "deleted-one"}},
		&bug069Req[*bug069Doc]{data: map[string]any{"name": "live-one"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	delID, liveID := (*created[0]).ULID, (*created[1]).ULID
	if err := db.Model(&bug069Doc{}).Where("ulid = ?", delID).Update("is_deleted", int8(1)).Error; err != nil {
		t.Fatalf("mark deleted: %v", err)
	}

	if err := svc.BatchUpdateByIDs(ctx, []any{delID, liveID}, map[string]any{"name": "batch"}); err != nil {
		t.Fatalf("BatchUpdateByIDs: %v", err)
	}

	var delRow, liveRow bug069Doc
	if err := db.Where("ulid = ?", delID).First(&delRow).Error; err != nil {
		t.Fatalf("query deleted row: %v", err)
	}
	if err := db.Where("ulid = ?", liveID).First(&liveRow).Error; err != nil {
		t.Fatalf("query live row: %v", err)
	}
	if delRow.Name != "deleted-one" {
		t.Errorf("BUG-069: deleted row must not be updated by BatchUpdateByIDs, name = %q", delRow.Name)
	}
	if liveRow.Name != "batch" {
		t.Errorf("live row must still be updated, name = %q", liveRow.Name)
	}
}

// TestBug069RestoreThenUpdate 恢复链路（BUG-069 配套能力）：
// 已删记录不可 update → Restore 之后才可 update，即「先恢复、再修改」。
func TestBug069RestoreThenUpdate(t *testing.T) {
	db := openBug069DB(t, &bug069Doc{})
	svc := NewGenericService[*bug069Doc](repository.NewCRUDWithDB[*bug069Doc](db), Config[*bug069Doc]{})
	ctx := context.WithValue(context.Background(), CtxKeyUserULID, "restorer-ulid")

	created, err := svc.Create(ctx, []CrudRequest[*bug069Doc]{
		&bug069Req[*bug069Doc]{data: map[string]any{"name": "origin"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := (*created[0]).ULID
	if err := svc.Delete(ctx, []any{id}, nil); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// 1) 已删 → update 被拒（写路径收口）
	if _, err := svc.Update(ctx, id, &bug069Req[*bug069Doc]{
		data: map[string]any{"name": "direct"},
	}); !goerrors.Is(err, errs.ErrRecordNotFound) {
		t.Fatalf("BUG-069: update before restore must be rejected, got %v", err)
	}

	// 2) Restore —— 只把软删字段置回未删值
	if err := svc.Restore(ctx, []any{id}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	var afterRestore bug069Doc
	if err := db.Where("ulid = ?", id).First(&afterRestore).Error; err != nil {
		t.Fatalf("query after restore: %v", err)
	}
	if afterRestore.IsDeleted != 0 {
		t.Errorf("is_deleted after restore = %d, want 0", afterRestore.IsDeleted)
	}
	if afterRestore.Name != "origin" {
		t.Errorf("restore must not modify business fields, name = %q want origin", afterRestore.Name)
	}

	// 3) 恢复后 → update 成功（先恢复、再修改）
	updated, err := svc.Update(ctx, id, &bug069Req[*bug069Doc]{data: map[string]any{"name": "modified"}})
	if err != nil {
		t.Fatalf("update after restore: %v", err)
	}
	if (*updated).Name != "modified" {
		t.Errorf("name after update = %q, want modified", (*updated).Name)
	}
}

// TestBug069RestoreUnsupportedEntityIntegration 物理删实体无恢复语义。
func TestBug069RestoreUnsupportedEntityIntegration(t *testing.T) {
	db := openBug069DB(t, &bug069NoDelDoc{})
	svc := NewGenericService[*bug069NoDelDoc](repository.NewCRUDWithDB[*bug069NoDelDoc](db), Config[*bug069NoDelDoc]{})
	if err := svc.Restore(context.Background(), []any{"x"}); !goerrors.Is(err, errs.ErrSoftDeleteNotSupported) {
		t.Errorf("Restore on physical-delete entity = %v, want ErrSoftDeleteNotSupported", err)
	}
}

// TestBug069RestoreIdempotentOnLiveRecord 对**未删除**记录调 Restore：
// 幂等成功，软删标记与业务字段均不变（heims 复核补充）。
func TestBug069RestoreIdempotentOnLiveRecord(t *testing.T) {
	db := openBug069DB(t, &bug069Doc{})
	svc := NewGenericService[*bug069Doc](repository.NewCRUDWithDB[*bug069Doc](db), Config[*bug069Doc]{})
	ctx := context.Background()

	created, err := svc.Create(ctx, []CrudRequest[*bug069Doc]{
		&bug069Req[*bug069Doc]{data: map[string]any{"name": "live"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := (*created[0]).ULID

	if err := svc.Restore(ctx, []any{id}); err != nil {
		t.Fatalf("Restore on live record must be idempotent success: %v", err)
	}
	var row bug069Doc
	if err := db.Where("ulid = ?", id).First(&row).Error; err != nil {
		t.Fatalf("query: %v", err)
	}
	if row.IsDeleted != 0 || row.Name != "live" {
		t.Errorf("live record must stay untouched, is_deleted=%d name=%q", row.IsDeleted, row.Name)
	}
}

// TestBug069RestoreMixedIDs ids 混合已删与未删：只恢复已删部分，不误伤未删记录。
func TestBug069RestoreMixedIDs(t *testing.T) {
	db := openBug069DB(t, &bug069Doc{})
	svc := NewGenericService[*bug069Doc](repository.NewCRUDWithDB[*bug069Doc](db), Config[*bug069Doc]{})
	ctx := context.Background()

	created, err := svc.Create(ctx, []CrudRequest[*bug069Doc]{
		&bug069Req[*bug069Doc]{data: map[string]any{"name": "deleted-one"}},
		&bug069Req[*bug069Doc]{data: map[string]any{"name": "live-one"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	delID, liveID := (*created[0]).ULID, (*created[1]).ULID
	if err := svc.Delete(ctx, []any{delID}, nil); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if err := svc.Restore(ctx, []any{delID, liveID}); err != nil {
		t.Fatalf("Restore mixed ids: %v", err)
	}
	var delRow, liveRow bug069Doc
	if err := db.Where("ulid = ?", delID).First(&delRow).Error; err != nil {
		t.Fatalf("query deleted row: %v", err)
	}
	if err := db.Where("ulid = ?", liveID).First(&liveRow).Error; err != nil {
		t.Fatalf("query live row: %v", err)
	}
	if delRow.IsDeleted != 0 {
		t.Errorf("deleted row must be restored, is_deleted = %d", delRow.IsDeleted)
	}
	if liveRow.IsDeleted != 0 || liveRow.Name != "live-one" {
		t.Errorf("live row must stay untouched, is_deleted=%d name=%q", liveRow.IsDeleted, liveRow.Name)
	}
}

// TestBug069RestoreUnknownIDNoOp ids 含不存在主键：无操作成功
// （与 BUG-052「空 ids 静默成功」语义对齐：SQL WHERE pk IN (...) 无命中）。
func TestBug069RestoreUnknownIDNoOp(t *testing.T) {
	db := openBug069DB(t, &bug069Doc{})
	svc := NewGenericService[*bug069Doc](repository.NewCRUDWithDB[*bug069Doc](db), Config[*bug069Doc]{})
	if err := svc.Restore(context.Background(), []any{"no-such-ulid"}); err != nil {
		t.Errorf("Restore with unknown id must be no-op success, got %v", err)
	}
}

// TestBug069RestoreVersionedEntityRejected 版本化实体：删除=废弃（不写 is_deleted），
// restore 对其恒为空操作 → 必须显式报错，避免调用方误以为恢复成功（复核 P2）。
func TestBug069RestoreVersionedEntityRejected(t *testing.T) {
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
	id := (*created[0]).ULID

	// 版本化删除 = 废弃（is_current=0 / deprecated），is_deleted 仍为 0
	if err := svc.Delete(ctx, []any{id}, nil); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := svc.Restore(ctx, []any{id}); !goerrors.Is(err, errs.ErrUseActivateInstead) {
		t.Errorf("Restore on versioned entity = %v, want ErrUseActivateInstead", err)
	}
	var row bug069VerDoc
	if err := db.Where("ulid = ?", id).First(&row).Error; err != nil {
		t.Fatalf("query: %v", err)
	}
	if row.IsCurrent != 0 {
		t.Errorf("restored-must-not-happen: is_current = %d, want 0 (still deprecated)", row.IsCurrent)
	}
}

// TestBug069UpdateDeprecatedVersionRejected 版本化实体：更新已废弃版本行
// （is_current=0）是绕过 activate 的复活通道 → 必须拒绝（复核 P3）。
func TestBug069UpdateDeprecatedVersionRejected(t *testing.T) {
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
	id, code := (*created[0]).ULID, (*created[0]).Code

	// 版本化删除 → is_current=0（废弃态），is_deleted 保持 0
	if err := svc.Delete(ctx, []any{id}, nil); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	var deprecated bug069VerDoc
	if err := db.Where("ulid = ?", id).First(&deprecated).Error; err != nil {
		t.Fatalf("query: %v", err)
	}
	if deprecated.IsCurrent != 0 || deprecated.IsDeleted != 0 {
		t.Fatalf("versioned delete must deprecate (is_current=0) without is_deleted, got current=%d deleted=%d",
			deprecated.IsCurrent, deprecated.IsDeleted)
	}

	if _, err := svc.Update(ctx, id, &bug069Req[*bug069VerDoc]{
		data: map[string]any{"name": "revived"},
	}); !goerrors.Is(err, errs.ErrUpdateDeprecatedVersion) {
		t.Fatalf("update deprecated version = %v, want ErrUpdateDeprecatedVersion", err)
	}
	var cnt int64
	if err := db.Model(&bug069VerDoc{}).Where("code = ?", code).Count(&cnt).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt != 1 {
		t.Errorf("no new version row must be derived, rows = %d want 1", cnt)
	}
}

// TestBug069UpdateCurrentVersionAllowed 不回归：版本化实体更新**当前**版本行
// （is_current=1，含草稿）仍可正常派生新版本。
func TestBug069UpdateCurrentVersionAllowed(t *testing.T) {
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
	id, code := (*created[0]).ULID, (*created[0]).Code
	if (*created[0]).IsCurrent != 1 {
		t.Fatalf("created version must be current, is_current = %d", (*created[0]).IsCurrent)
	}

	updated, err := svc.Update(ctx, id, &bug069Req[*bug069VerDoc]{data: map[string]any{"name": "v2"}})
	if err != nil {
		t.Fatalf("update current version must be allowed: %v", err)
	}
	if (*updated).Name != "v2" {
		t.Errorf("name = %q, want v2", (*updated).Name)
	}
	var cnt int64
	if err := db.Model(&bug069VerDoc{}).Where("code = ?", code).Count(&cnt).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt != 2 {
		t.Errorf("versioned update must derive a new version row, rows = %d want 2", cnt)
	}
}

// TestBug069RestoreWritesOpLog 恢复是安全相关状态迁移：EnableOpLog 时
// 必须写 operation="restore"（与 update / delete 对齐，复核建议）。
func TestBug069RestoreWritesOpLog(t *testing.T) {
	db := openBug069DB(t, &bug069Doc{}, &entity.SysOperationLog{})
	svc := NewGenericService[*bug069Doc](repository.NewCRUDWithDB[*bug069Doc](db), Config[*bug069Doc]{
		EnableOpLog: true,
		EntityName:  "bug069_doc",
	})
	svc.SetOpLogRepo(repository.NewCRUDWithDB[entity.SysOperationLog](db))
	ctx := context.WithValue(context.Background(), CtxKeyUserULID, "operator-ulid")

	created, err := svc.Create(ctx, []CrudRequest[*bug069Doc]{
		&bug069Req[*bug069Doc]{data: map[string]any{"name": "x"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := (*created[0]).ULID
	if err := svc.Delete(ctx, []any{id}, nil); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := svc.Restore(ctx, []any{id}); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	var logs []entity.SysOperationLog
	if err := db.Where("entity_id = ? AND operation = ?", id, "restore").Find(&logs).Error; err != nil {
		t.Fatalf("query op log: %v", err)
	}
	if len(logs) == 0 {
		t.Fatal("BUG-069: restore must write an op-log entry with operation=restore")
	}
	if logs[0].OperatorULID != "operator-ulid" {
		t.Errorf("op-log operator = %q, want operator-ulid", logs[0].OperatorULID)
	}
	if logs[0].EntityType != "bug069_doc" {
		t.Errorf("op-log entity_type = %q, want bug069_doc", logs[0].EntityType)
	}
}

// TestBug069SameSoftDeleteValueCrossTypes 用例 8（纯函数）：
// 软删值比较必须跨类型成立 —— 实体字段类型（int8/int/bool/string）与
// DeletedValue 默认类型（int8(0)）不一致时，不能把正常记录误判成已删。
func TestBug069SameSoftDeleteValueCrossTypes(t *testing.T) {
	same := []struct{ a, b any }{
		{int8(0), int8(0)}, // 典型：IsDeleted int8 vs 默认 DeletedValue
		{int8(0), int(0)},  // 实体 int8 vs 配置 int
		{int(0), int8(0)},  // 反向
		{false, int8(0)},   // bool 软删列 vs 默认 int8(0)
		{true, int8(1)},
		{"n", "n"}, // 字符串软删列
		{"", ""},
	}
	for _, c := range same {
		if !sameSoftDeleteValue(c.a, c.b) {
			t.Errorf("sameSoftDeleteValue(%v,%v) = false, want true (live record must not be treated as deleted)", c.a, c.b)
		}
	}
	diff := []struct{ a, b any }{
		{int8(1), int8(0)}, // 已删
		{int(1), int8(0)},
		{true, int8(0)}, // bool 已删 vs 未删值 0
		{"y", "n"},
	}
	for _, c := range diff {
		if sameSoftDeleteValue(c.a, c.b) {
			t.Errorf("sameSoftDeleteValue(%v,%v) = true, want false (deleted record must be detected)", c.a, c.b)
		}
	}
}
