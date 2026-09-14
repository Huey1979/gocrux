// BUG-077 回归测试：操作日志（opLog）与备份写入器（bakWriter）接线。
//
// 缺陷要点（修复前）：
//  1. 唯一的 opLog 注入口 SetOpLogRepo 的参数类型位于 internal/，模块外无法命名
//     —— 对外救活靠新增的 SetOpLogDB / SetOpLogWriter（公开类型）。
//  2. EnableOpLog=true 且未注入写入方时**完全静默**（不报错、不告警、零记录）。
//  3. 写失败被 `_ =` 丢弃（审计日志静默失效）。
//  4. bakWriter 被塞进同一个 `if EnableOpLog && opLogRepo != nil`，使「非版本化 update
//     的旧值备份」在未开审计时永远写不出；四个调用点门控口径互不相同。
//
// 本文件覆盖：① SetOpLogDB 可用；② SetOpLogWriter 自定义实现可用（含前后快照）；
// ③ 未注入时静默跳过（不 panic、不阻塞业务）；④ bakWriter 与 opLog 解耦（核心回归）；
// ⑤ 物理删除备份同样解耦；⑥ 写失败可见（错误日志，不阻塞主流程）；
// ⑦ UpdatePair / AsUpdatePair 对外可断言。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Huey1979/gocrux/internal/model/entity"
	"github.com/Huey1979/gocrux/repository"

	"github.com/sirupsen/logrus"
)

// bug077Doc 非版本化实体（支持软删）。
type bug077Doc struct {
	ULID      string    `gorm:"column:ulid;primaryKey;size:26" json:"ulid"`
	Name      string    `gorm:"column:name;size:100" json:"name"`
	IsDeleted int8      `gorm:"column:is_deleted;default:0" json:"-"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updated_at"`
	UpdatedBy string    `gorm:"column:updated_by;size:26" json:"updated_by"`
}

func (d *bug077Doc) SetDefaults()             {}
func (d *bug077Doc) SetCreatedAt(_ time.Time) {}
func (d *bug077Doc) SetCreatedBy(string)      {}
func (d *bug077Doc) SetUpdatedAt(t time.Time) { d.UpdatedAt = t }
func (d *bug077Doc) SetUpdatedBy(uid string)  { d.UpdatedBy = uid }
func (d *bug077Doc) SupportsDraft() bool      { return false }
func (d *bug077Doc) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *bug077Doc) PKField() string          { return "ulid" }
func (d *bug077Doc) SelfFKField() string      { return "" }

// bug077NoDelDoc 无软删列实体：删除走物理删 + 备份日志，用于覆盖 §8.2 的删除门控。
type bug077NoDelDoc struct {
	ULID string `gorm:"column:ulid;primaryKey;size:26" json:"ulid"`
	Name string `gorm:"column:name;size:100" json:"name"`
}

func (d *bug077NoDelDoc) SetDefaults()             {}
func (d *bug077NoDelDoc) SetCreatedAt(_ time.Time) {}
func (d *bug077NoDelDoc) SetCreatedBy(string)      {}
func (d *bug077NoDelDoc) SetUpdatedAt(_ time.Time) {}
func (d *bug077NoDelDoc) SetUpdatedBy(string)      {}
func (d *bug077NoDelDoc) SupportsDraft() bool      { return false }
func (d *bug077NoDelDoc) SetDelete() bool          { return false }
func (d *bug077NoDelDoc) PKField() string          { return "ulid" }
func (d *bug077NoDelDoc) SelfFKField() string      { return "" }

// bugReq 泛型请求体（JSON 中介合并），与 bug069 的 bug069Req 同形，独立命名避免耦合。
type bugReq[M Record] struct{ data map[string]any }

func (r *bugReq[M]) MergeTo(target *M) error {
	if len(r.data) == 0 {
		return nil
	}
	b, err := json.Marshal(r.data)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, *target)
}
func (r *bugReq[M]) GetID() any           { return nil }
func (r *bugReq[M]) Validate() error      { return nil }
func (r *bugReq[M]) Data() map[string]any { return r.data }

// recordingOpLogWriter 记录写入内容的测试写入方；可配置返回错误。
type recordingOpLogWriter struct {
	mu      sync.Mutex
	records []OpLogRecord
	err     error
}

func (w *recordingOpLogWriter) WriteOpLogs(_ context.Context, records []OpLogRecord) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	w.records = append(w.records, records...)
	return nil
}

func (w *recordingOpLogWriter) snapshot() []OpLogRecord {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]OpLogRecord, len(w.records))
	copy(out, w.records)
	return out
}

// captureLogHook 捕获 logrus Error 级日志，证明 op-log 写失败不再被吞。
type captureLogHook struct {
	mu      sync.Mutex
	entries []logrus.Entry
}

func (h *captureLogHook) Levels() []logrus.Level { return []logrus.Level{logrus.ErrorLevel} }

func (h *captureLogHook) Fire(e *logrus.Entry) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries = append(h.entries, *e)
	return nil
}

func (h *captureLogHook) hasError(substr string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, e := range h.entries {
		if strings.Contains(e.Message, substr) {
			return true
		}
	}
	return false
}

// TestBug077SetOpLogDBWritesRecords 用例 1：SetOpLogDB 是模块外可用的公开注入口。
//
// 修复前模块外无法构造 SetOpLogRepo 的参数（internal 类型）；本用例同时验证
// 「注入后 create / update / delete 都真的落库」。
func TestBug077SetOpLogDBWritesRecords(t *testing.T) {
	db := openBug069DB(t, &bug077Doc{}, &entity.SysOperationLog{})
	svc := NewGenericService[*bug077Doc](repository.NewCRUDWithDB[*bug077Doc](db), Config[*bug077Doc]{
		EntityName:  "bug077_doc",
		EnableOpLog: true,
	})
	svc.SetOpLogDB(db) // BUG-077：公开注入口（*gorm.DB 可命名）
	ctx := context.WithValue(context.Background(), CtxKeyUserULID, "op-ulid")

	created, err := svc.Create(ctx, []CrudRequest[*bug077Doc]{
		&bugReq[*bug077Doc]{data: map[string]any{"name": "a"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := (*created[0]).ULID
	if _, err := svc.Update(ctx, id, &bugReq[*bug077Doc]{data: map[string]any{"name": "b"}}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := svc.Delete(ctx, []any{id}, nil); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	var logs []entity.SysOperationLog
	if err := db.Where("entity_type = ?", "bug077_doc").Order("operated_at").Find(&logs).Error; err != nil {
		t.Fatalf("query op log: %v", err)
	}
	if len(logs) != 3 {
		t.Fatalf("op-log rows = %d, want 3 (create/update/delete)", len(logs))
	}
	wantOps := []string{"create", "update", "delete"}
	for i, want := range wantOps {
		if logs[i].Operation != want {
			t.Errorf("op-log[%d].operation = %q, want %q", i, logs[i].Operation, want)
		}
		if logs[i].OperatorULID != "op-ulid" {
			t.Errorf("op-log[%d].operator = %q, want op-ulid", i, logs[i].OperatorULID)
		}
		if logs[i].LogULID == "" {
			t.Errorf("op-log[%d] must have a generated log_ulid", i)
		}
	}
}

// TestBug077SetOpLogWriterCustomImpl 用例 2：自定义写入方（如 Mongo）可接管落库，
// 且能拿到前后快照（heims 需要的形态）。
func TestBug077SetOpLogWriterCustomImpl(t *testing.T) {
	db := openBug069DB(t, &bug077Doc{})
	writer := &recordingOpLogWriter{}
	svc := NewGenericService[*bug077Doc](repository.NewCRUDWithDB[*bug077Doc](db), Config[*bug077Doc]{
		EntityName:  "bug077_doc",
		EnableOpLog: true,
	})
	svc.SetOpLogWriter(writer)
	ctx := context.Background()

	created, err := svc.Create(ctx, []CrudRequest[*bug077Doc]{
		&bugReq[*bug077Doc]{data: map[string]any{"name": "before"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := (*created[0]).ULID
	if _, err := svc.Update(ctx, id, &bugReq[*bug077Doc]{data: map[string]any{"name": "after"}}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	recs := writer.snapshot()
	if len(recs) != 2 {
		t.Fatalf("custom writer got %d records, want 2", len(recs))
	}
	if recs[1].Operation != "update" {
		t.Fatalf("records[1].operation = %q, want update", recs[1].Operation)
	}
	if recs[1].EntityType != "bug077_doc" {
		t.Errorf("records[1].entity_type = %q, want bug077_doc", recs[1].EntityType)
	}
	// 快照：update 必须同时带旧值（被覆盖的那份）与新值
	if len(recs[1].RecordBefore) == 0 {
		t.Error("BUG-077: update op-log record must carry RecordBefore snapshot")
	} else if !strings.Contains(string(recs[1].RecordBefore), "\"before\"") {
		t.Errorf("RecordBefore = %s, want it to contain old value \"before\"", recs[1].RecordBefore)
	}
	if len(recs[1].RecordAfter) == 0 || !strings.Contains(string(recs[1].RecordAfter), "\"after\"") {
		t.Errorf("RecordAfter = %s, want it to contain new value \"after\"", recs[1].RecordAfter)
	}
}

// TestBug077NoWriterIsSilentButSafe 用例 3：EnableOpLog=true 但未注入写入方时
// 不 panic、不阻塞业务（构造期告警由 warnOpLogMisconfigured 负责）。
func TestBug077NoWriterIsSilentButSafe(t *testing.T) {
	db := openBug069DB(t, &bug077Doc{})
	svc := NewGenericService[*bug077Doc](repository.NewCRUDWithDB[*bug077Doc](db), Config[*bug077Doc]{
		EntityName:  "bug077_doc",
		EnableOpLog: true, // 故意不注入
	})
	if svc.opLogReady() {
		t.Fatal("opLogReady must be false when no writer is injected")
	}
	ctx := context.Background()
	created, err := svc.Create(ctx, []CrudRequest[*bug077Doc]{
		&bugReq[*bug077Doc]{data: map[string]any{"name": "x"}},
	})
	if err != nil {
		t.Fatalf("business write must not fail when op-log is misconfigured: %v", err)
	}
	if _, err := svc.Update(ctx, (*created[0]).ULID, &bugReq[*bug077Doc]{data: map[string]any{"name": "y"}}); err != nil {
		t.Fatalf("Update must not fail when op-log is misconfigured: %v", err)
	}
}

// TestBug077BakWriterDecoupledFromOpLog 用例 4（核心回归 §8.1/§8.2）：
// 只注册 bakWriter、**不开** EnableOpLog 时，非版本化 update 的旧值备份必须照常写出。
//
// 修复前该路径被 `if EnableOpLog && opLogRepo != nil` 锁死 → 备份零条。
func TestBug077BakWriterDecoupledFromOpLog(t *testing.T) {
	db := openBug069DB(t, &bug077Doc{})
	var backups []any
	svc := NewGenericService[*bug077Doc](repository.NewCRUDWithDB[*bug077Doc](db), Config[*bug077Doc]{
		EntityName:  "bug077_doc",
		EnableOpLog: false, // 关键：不开审计
	})
	svc.SetBakWriter(func(_ context.Context, _ string, _ any, _ string, oldData any, _ string) error {
		backups = append(backups, oldData)
		return nil
	})
	ctx := context.Background()

	created, err := svc.Create(ctx, []CrudRequest[*bug077Doc]{
		&bugReq[*bug077Doc]{data: map[string]any{"name": "old-name"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := (*created[0]).ULID
	if _, err := svc.Update(ctx, id, &bugReq[*bug077Doc]{data: map[string]any{"name": "new-name"}}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if len(backups) != 1 {
		t.Fatalf("BUG-077: non-versioned update backup = %d, want 1 (bakWriter must not depend on EnableOpLog)", len(backups))
	}
	// M = *bug077Doc ⇒ UpdatePair.Old 的静态类型是 **bug077Doc（与框架既有形态一致）。
	// 断言重点在**内容**：必须是「被覆盖前的旧值」（浅拷贝会让快照被 MergeTo 就地改写）。
	old, ok := backups[0].(**bug077Doc)
	if !ok {
		t.Fatalf("backup payload type = %T, want **bug077Doc", backups[0])
	}
	if old == nil || *old == nil {
		t.Fatal("BUG-077: backup old snapshot must not be nil")
	}
	if (*old).Name != "old-name" {
		t.Errorf("BUG-077: backup must hold the OLD value, name = %q want old-name "+
			"(浅拷贝会让快照被 MergeTo 就地改写)", (*old).Name)
	}
	var live bug077Doc
	if err := db.Where("ulid = ?", id).First(&live).Error; err != nil {
		t.Fatalf("query live row: %v", err)
	}
	if live.Name != "new-name" {
		t.Errorf("live name = %q, want new-name", live.Name)
	}
}

// TestBug077BakWriterOnHardDeleteWithoutOpLog 用例 5（§8.2 口径统一）：
// 物理删除路径的备份同样不受 EnableOpLog 影响。
func TestBug077BakWriterOnHardDeleteWithoutOpLog(t *testing.T) {
	db := openBug069DB(t, &bug077NoDelDoc{})
	var ops []string
	svc := NewGenericService[*bug077NoDelDoc](repository.NewCRUDWithDB[*bug077NoDelDoc](db), Config[*bug077NoDelDoc]{
		EntityName:  "bug077_nodel",
		EnableOpLog: false,
	})
	svc.SetBakWriter(func(_ context.Context, _ string, _ any, operation string, _ any, _ string) error {
		ops = append(ops, operation)
		return nil
	})
	ctx := context.Background()

	created, err := svc.Create(ctx, []CrudRequest[*bug077NoDelDoc]{
		&bugReq[*bug077NoDelDoc]{data: map[string]any{"name": "gone"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.Delete(ctx, []any{(*created[0]).ULID}, nil); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(ops) != 1 || ops[0] != "delete" {
		t.Fatalf("BUG-077: hard-delete backup ops = %v, want [delete] (independent of EnableOpLog)", ops)
	}
	var cnt int64
	if err := db.Model(&bug077NoDelDoc{}).Count(&cnt).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt != 0 {
		t.Errorf("rows after hard delete = %d, want 0", cnt)
	}
}

// TestBug077WriterErrorIsNotSilent 用例 6（§2.3）：写入方返回错误时，
// 主业务不受影响，但错误必须被记录（不吞）。
func TestBug077WriterErrorIsNotSilent(t *testing.T) {
	db := openBug069DB(t, &bug077Doc{})
	writer := &recordingOpLogWriter{err: errors.New("mongo down")}
	svc := NewGenericService[*bug077Doc](repository.NewCRUDWithDB[*bug077Doc](db), Config[*bug077Doc]{
		EntityName:  "bug077_doc",
		EnableOpLog: true,
	})
	svc.SetOpLogWriter(writer)

	// 用 logrus hook 捕获 Error 级日志，证明失败可见（修复前是 `_ =` 全吞）
	hook := &captureLogHook{}
	logrus.AddHook(hook)
	defer logrus.StandardLogger().ReplaceHooks(nil)

	created, err := svc.Create(context.Background(), []CrudRequest[*bug077Doc]{
		&bugReq[*bug077Doc]{data: map[string]any{"name": "x"}},
	})
	if err != nil {
		t.Fatalf("op-log failure must not fail the business write: %v", err)
	}
	if (*created[0]).ULID == "" {
		t.Fatal("create must still persist the record")
	}
	if !hook.hasError("写操作日志失败") {
		t.Error("BUG-077: op-log write failure must be logged (was silently discarded)")
	}
}

// TestBug077AsUpdatePairExported 用例 7（§8.3）：应用可在钩子里对外部类型断言取旧值，
// 无需反射掏框架内部结构。
func TestBug077AsUpdatePairExported(t *testing.T) {
	oldRec := &bug077Doc{ULID: "u1", Name: "old"}
	newRec := &bug077Doc{ULID: "u1", Name: "new"}
	got, ok := AsUpdatePair[*bug077Doc](&UpdatePair[*bug077Doc]{Old: &oldRec, New: &newRec})
	if !ok || got == nil {
		t.Fatal("BUG-077: AsUpdatePair must resolve an exported *UpdatePair")
	}
	if got.Old != &oldRec || got.New != &newRec {
		t.Fatalf("AsUpdatePair returned wrong pair: old=%v new=%v", got.Old, got.New)
	}
	// 形态不符（如 editVersion 上下文 / nil）时不 panic、ok=false
	if _, ok := AsUpdatePair[*bug077Doc](struct{}{}); ok {
		t.Error("AsUpdatePair must report ok=false for foreign payloads")
	}
	if _, ok := AsUpdatePair[*bug077Doc](nil); ok {
		t.Error("AsUpdatePair must report ok=false for nil payload")
	}
}
