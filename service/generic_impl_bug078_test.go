// BUG-078 回归测试（前半）：knownColumns 不递归匿名嵌入字段，
// 导致 created_at / updated_at / created_by / updated_by 不在过滤列白名单里，
// `created_at:*` 条件被**整条静默丢弃并返回全量数据、不报错**。
//
// 缺陷：knownColumns 只遍历 t.NumField() 的顶层字段；而审计字段一律是匿名嵌入
// （AuditFields / MongoAuditFields），嵌入字段自身没有 gorm/json 标签、
// bson 标签是 ",inline"（parseBsonKey 取逗号前段为空串）⇒ 白名单里一个审计列都没有。
// 于是文档 §1.6 给的示例 `?created_at:lte=2026-01-01` 在所有实体上都是空操作。
//
// 修复：knownColumns 递归展开匿名嵌入 struct（限深 3 层防环）。
//
// 本文件为纯函数测试（不需要数据库）。
package service

import (
	"testing"
	"time"
)

// bug078AuditFields 模拟 heims 的 MySQL 审计字段（匿名嵌入，gorm tag）。
type bug078AuditFields struct {
	CreatedBy string    `gorm:"column:created_by;size:26" json:"created_by"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedBy string    `gorm:"column:updated_by;size:26" json:"updated_by"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updated_at"`
}

// bug078MongoAuditFields 模拟 heims 的 Mongo 审计字段（匿名嵌入，bson:",inline"）。
type bug078MongoAuditFields struct {
	CreatedBy string    `bson:"created_by" json:"created_by"`
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedBy string    `bson:"updated_by" json:"updated_by"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
}

// bug078MySQLEntity 带匿名嵌入审计字段的 MySQL 实体。
type bug078MySQLEntity struct {
	bug078AuditFields
	ULID   string `gorm:"column:log_ulid;primaryKey;size:26" json:"log_ulid"`
	Status string `gorm:"column:status;size:20" json:"status"`
}

func (d *bug078MySQLEntity) SetDefaults()             {}
func (d *bug078MySQLEntity) SetCreatedAt(_ time.Time) {}
func (d *bug078MySQLEntity) SetCreatedBy(string)      {}
func (d *bug078MySQLEntity) SetUpdatedAt(_ time.Time) {}
func (d *bug078MySQLEntity) SetUpdatedBy(string)      {}
func (d *bug078MySQLEntity) SupportsDraft() bool      { return false }
func (d *bug078MySQLEntity) SetDelete() bool          { return false }
func (d *bug078MySQLEntity) PKField() string          { return "log_ulid" }
func (d *bug078MySQLEntity) SelfFKField() string      { return "" }

// bug078MongoEntity 带匿名嵌入审计字段的 Mongo 实体（bson:",inline"）。
type bug078MongoEntity struct {
	bug078MongoAuditFields `bson:",inline"`
	ULID                   string `bson:"log_ulid" json:"log_ulid"`
	TaskCode               string `bson:"task_code" json:"task_code"`
}

func (d *bug078MongoEntity) SetDefaults()             {}
func (d *bug078MongoEntity) SetCreatedAt(_ time.Time) {}
func (d *bug078MongoEntity) SetCreatedBy(string)      {}
func (d *bug078MongoEntity) SetUpdatedAt(_ time.Time) {}
func (d *bug078MongoEntity) SetUpdatedBy(string)      {}
func (d *bug078MongoEntity) SupportsDraft() bool      { return false }
func (d *bug078MongoEntity) SetDelete() bool          { return false }
func (d *bug078MongoEntity) PKField() string          { return "log_ulid" }
func (d *bug078MongoEntity) SelfFKField() string      { return "" }

// TestBug078KnownColumnsIncludesEmbeddedAuditMySQL 核心回归（MySQL）：
// 嵌入审计字段的四个列必须进入白名单。
func TestBug078KnownColumnsIncludesEmbeddedAuditMySQL(t *testing.T) {
	cols := knownColumns[*bug078MySQLEntity]()
	for _, want := range []string{"created_by", "created_at", "updated_by", "updated_at"} {
		if !cols[want] {
			t.Errorf("BUG-078: embedded audit column %q must be in knownColumns (否则过滤条件被静默丢弃)", want)
		}
	}
	// 实体自有列照常
	for _, want := range []string{"log_ulid", "status", "id"} {
		if !cols[want] {
			t.Errorf("own column %q must be in knownColumns", want)
		}
	}
}

// TestBug078KnownColumnsIncludesEmbeddedAuditMongo 核心回归（Mongo，bson:",inline"）：
// 原实现因 parseBsonKey(",inline") 返回空串而跳过整个嵌入字段。
func TestBug078KnownColumnsIncludesEmbeddedAuditMongo(t *testing.T) {
	cols := knownColumns[*bug078MongoEntity]()
	for _, want := range []string{"created_by", "created_at", "updated_by", "updated_at"} {
		if !cols[want] {
			t.Errorf("BUG-078: embedded audit column %q must be in knownColumns", want)
		}
	}
	for _, want := range []string{"log_ulid", "task_code", "id"} {
		if !cols[want] {
			t.Errorf("own column %q must be in knownColumns", want)
		}
	}
	// bson:",inline" 自身不得产生 "" 之类的垃圾键
	if _, bad := cols[""]; bad {
		t.Error("BUG-078: empty-string key must not be produced by \",inline\" tag")
	}
}

// bug078unexportedAudit 未导出的嵌入类型（同包内可嵌入）。
// 关键：`f.PkgPath != ""` 对匿名嵌入的**未导出类型**成立，
// 若先做该检查就会跳过整个嵌入字段 —— 这正是修复中把
// Anonymous 分支前移的原因。
type bug078unexportedAudit struct {
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

type bug078UnexportedEmbedEntity struct {
	bug078unexportedAudit
	ULID string `gorm:"column:ulid;primaryKey;size:26" json:"ulid"`
}

func (d *bug078UnexportedEmbedEntity) SetDefaults()             {}
func (d *bug078UnexportedEmbedEntity) SetCreatedAt(_ time.Time) {}
func (d *bug078UnexportedEmbedEntity) SetCreatedBy(string)      {}
func (d *bug078UnexportedEmbedEntity) SetUpdatedAt(_ time.Time) {}
func (d *bug078UnexportedEmbedEntity) SetUpdatedBy(string)      {}
func (d *bug078UnexportedEmbedEntity) SupportsDraft() bool      { return false }
func (d *bug078UnexportedEmbedEntity) SetDelete() bool          { return false }
func (d *bug078UnexportedEmbedEntity) PKField() string          { return "ulid" }
func (d *bug078UnexportedEmbedEntity) SelfFKField() string      { return "" }

// TestBug078KnownColumnsUnexportedEmbedded 未导出嵌入类型也必须展开
// （`f.PkgPath != ""` 不能拦在 Anonymous 判断之前）。
func TestBug078KnownColumnsUnexportedEmbedded(t *testing.T) {
	cols := knownColumns[*bug078UnexportedEmbedEntity]()
	if !cols["created_at"] {
		t.Error("BUG-078: embedded (unexported type) column created_at must be in knownColumns")
	}
	if !cols["ulid"] {
		t.Error("own column ulid must be in knownColumns")
	}
}

// TestBug078KnownColumnsNoInlineGarbage 不回归：
// `bson:"xxx,omitempty"` 仍取逗号前段（BUG-061），不产生带选项的键。
func TestBug078KnownColumnsNoInlineGarbage(t *testing.T) {
	cols := knownColumns[*bug078MongoEntity]()
	for k := range cols {
		if k == "" || len(k) > 0 && (k[0] == ',' || containsByte(k, ',')) {
			t.Errorf("BUG-078/BUG-061: unexpected column key %q", k)
		}
	}
}

func containsByte(s string, b byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return true
		}
	}
	return false
}

// ============================================================
// 递归限深（防自引用死循环）
// ============================================================

// bug078SelfRef 自引用结构（匿名嵌入自身指针）—— 递归必须有深度上限。
type bug078SelfRef struct {
	*bug078SelfRef
	Leaf string `gorm:"column:leaf"`
}

func (d *bug078SelfRef) SetDefaults()             {}
func (d *bug078SelfRef) SetCreatedAt(_ time.Time) {}
func (d *bug078SelfRef) SetCreatedBy(string)      {}
func (d *bug078SelfRef) SetUpdatedAt(_ time.Time) {}
func (d *bug078SelfRef) SetUpdatedBy(string)      {}
func (d *bug078SelfRef) SupportsDraft() bool      { return false }
func (d *bug078SelfRef) SetDelete() bool          { return false }
func (d *bug078SelfRef) PKField() string          { return "leaf" }
func (d *bug078SelfRef) SelfFKField() string      { return "" }

// TestBug078KnownColumnsRecursionTerminates 递归限深：
// 自引用结构不得导致无限递归（能正常返回即证明限深有效）。
func TestBug078KnownColumnsRecursionTerminates(t *testing.T) {
	done := make(chan map[string]bool, 1)
	go func() {
		done <- knownColumns[*bug078SelfRef]()
	}()
	select {
	case cols := <-done:
		if !cols["leaf"] {
			t.Error("own column leaf must be present")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("BUG-078: knownColumns recursion did not terminate (depth limit broken)")
	}
}
