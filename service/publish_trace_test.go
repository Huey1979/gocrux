// 发布痕迹（publish trace）回归测试。
//
// REQ: `edit-version` 把 `version_status` 改成 published 时，框架应同样留发布痕迹。
//
// 核心事实：「一个版本变成线上生效版本」共有 4 条路径，此前只有 activate 一条
// 由框架写 published_at / published_by / 发布历史；edit-version（deprecated →
// published，即「复活已废弃版本」）完全不留痕，且应用侧补不了 ——
// AfterEditVersion 钩子签名只给新值，拿不到旧状态。
//
// 本文件按 REQ §六「验收建议」逐条覆盖：
//
//	① edit-version: deprecated → published → published_at 非零、published_by=当前登录人、
//	   发布历史多一条且 via=edit-version；
//	② 反向：published → deprecated（下线）不得写 published_at、不得产生发布记录；
//	③ 反向：只改备注、状态不变时两项均不变（防回归）；
//	④ activate 路径既有留痕行为逐字不变（回归保护）；
//	⑤ 四条路径各触发一次且仅一次 OnPublished（构造「保存并发布」，验证不重复触发）。
package service

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Huey1979/gocrux/repository"
	"gorm.io/gorm"
)

// ============================================================
// 测试装置
// ============================================================

// publishTraceDoc 版本化实体，含发布痕迹基础列（M = *publishTraceDoc）。
type publishTraceDoc struct {
	ULID          string     `gorm:"column:ulid;primaryKey;size:26" json:"ulid"`
	Code          string     `gorm:"column:code;size:64" json:"code"`
	Name          string     `gorm:"column:name;size:100" json:"name"`
	VersionCode   string     `gorm:"column:version_code;size:20" json:"version_code"`
	VersionStatus string     `gorm:"column:version_status;size:20" json:"version_status"`
	VersionRemark string     `gorm:"column:version_remark;size:200" json:"version_remark"`
	IsCurrent     int8       `gorm:"column:is_current;default:0" json:"is_current"`
	ParentULID    string     `gorm:"column:parent_ulid;size:26" json:"parent_ulid"`
	CreatedBy     string     `gorm:"column:created_by;size:26" json:"created_by"`
	UpdatedAt     time.Time  `gorm:"column:updated_at" json:"updated_at"`
	PublishedAt   *time.Time `gorm:"column:published_at" json:"published_at"`
	PublishedBy   string     `gorm:"column:published_by;size:26" json:"published_by"`
	IsDeleted     int8       `gorm:"column:is_deleted;default:0" json:"-"`
}

func (d *publishTraceDoc) SetDefaults()             {}
func (d *publishTraceDoc) SetCreatedAt(_ time.Time) {}
func (d *publishTraceDoc) SetCreatedBy(uid string)  { d.CreatedBy = uid }
func (d *publishTraceDoc) SetUpdatedAt(t time.Time) { d.UpdatedAt = t }
func (d *publishTraceDoc) SetUpdatedBy(string)      {}
func (d *publishTraceDoc) SupportsDraft() bool      { return true }
func (d *publishTraceDoc) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *publishTraceDoc) PKField() string          { return "ulid" }
func (d *publishTraceDoc) SelfFKField() string      { return "" }

type publishTraceReq[M Record] struct{ data map[string]any }

func (r *publishTraceReq[M]) MergeTo(target *M) error { return publishTraceMerge(r.data, target) }

// MergeToExisting 把请求字段合并到**已有实体**上（而不是替换整个实体）。
//
// 关键：target 是 *M，而 M 本身可能是指针（如 *publishTraceDoc），
// 直接 json.Unmarshal 到 target 会新建目标对象、丢掉基座旧行的字段 ——
// 版本化 update 的「保留旧值 + 覆盖请求字段」语义随即失效（实测表现为
// 用户传入的 version_status=published 被基座新对象的空值冲掉，回落到 draft）。
// MergeToExisting 把请求字段合并到**已有实体**上（而不是替换整个实体）。
//
// 关键：不能直接 json.Unmarshal 到 target —— target 是 *M，而 M 本身是指针
// （如 *publishTraceDoc），直接反序列化会新建目标对象并丢弃基座旧行字段，
// 使「保留旧值 + 覆盖请求字段」的更新语义失效。
// 这里改为「基座 JSON → 用请求覆盖 → 写回同一对象」。
func (r *publishTraceReq[M]) MergeToExisting(target *M) error {
	base, err := json.Marshal(*target)
	if err != nil {
		return err
	}
	var merged map[string]any
	if err := json.Unmarshal(base, &merged); err != nil {
		return err
	}
	for k, v := range r.data {
		merged[k] = v
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, *target)
}

func (r *publishTraceReq[M]) GetID() any           { return nil }
func (r *publishTraceReq[M]) Validate() error      { return nil }
func (r *publishTraceReq[M]) Data() map[string]any { return r.data }

func publishTraceMerge[M any](m map[string]any, target *M) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, target)
}

// memPublishHistoryWriter 内存写入方：测试断言用（避免依赖 MongoDB）。
type memPublishHistoryWriter struct {
	mu      sync.Mutex
	records []PublishHistoryRecord
}

func (w *memPublishHistoryWriter) WritePublishHistories(_ context.Context, records []PublishHistoryRecord) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.records = append(w.records, records...)
	return nil
}

func (w *memPublishHistoryWriter) all() []PublishHistoryRecord {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]PublishHistoryRecord(nil), w.records...)
}

// newPublishTraceSvc 构造带发布痕迹观测装置的服务。
func newPublishTraceSvc(t *testing.T) (
	*GenericService[*publishTraceDoc], *gorm.DB, *memPublishHistoryWriter, *[]PublishVia,
) {
	t.Helper()
	db := openBug069DB(t, &publishTraceDoc{})
	svc := NewGenericService[*publishTraceDoc](repository.NewCRUDWithDB[*publishTraceDoc](db), Config[*publishTraceDoc]{
		EntityName:  "publish_trace_doc",
		VersionMode: true,
		VersionFields: &VersionFieldMapping{
			ULIDField: "ULID", CodeField: "Code", VersionField: "VersionCode",
			CurrentField: "IsCurrent", StatusField: "VersionStatus",
			ParentField: "ParentULID", PublishedAtField: "PublishedAt",
			PublishedByField: "PublishedBy",
		},
	})
	w := &memPublishHistoryWriter{}
	svc.SetPublishHistoryWriter(w)

	var mu sync.Mutex
	vias := make([]PublishVia, 0, 4)
	svc.SetOnPublished(func(_ context.Context, _ any, _ **publishTraceDoc, via PublishVia) error {
		mu.Lock()
		defer mu.Unlock()
		vias = append(vias, via)
		return nil
	})
	return svc, db, w, &vias
}

// traceCtx 带登录身份的 ctx（PublishedBy 断言需要）。
func traceCtx(user string) context.Context {
	return context.WithValue(context.Background(), CtxKeyUserULID, user)
}

// seedDeprecated 造一条 deprecated 的版本行（edit-version 的合法起点）。
func seedDeprecated(t *testing.T, db *gorm.DB, ulid, code string) {
	t.Helper()
	past := time.Now().Add(-24 * time.Hour)
	row := &publishTraceDoc{
		ULID: ulid, Code: code, Name: "old", VersionCode: "v1.0",
		VersionStatus: string(VersionStatusDeprecated), IsCurrent: 0,
		PublishedAt: &past, PublishedBy: "original-publisher",
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("seed deprecated: %v", err)
	}
}

// ============================================================
// ① 核心：edit-version 把 deprecated 改成 published 应留痕
// ============================================================

func TestPublishTraceEditVersionResurrectWritesTrace(t *testing.T) {
	svc, db, w, vias := newPublishTraceSvc(t)
	ctx := traceCtx("user-A")
	seedDeprecated(t, db, "v-old", "CODE1")

	deadline := time.Now()
	result, err := svc.EditVersion(ctx, "v-old", map[string]any{
		"version_status": string(VersionStatusPublished),
	})
	if err != nil {
		t.Fatalf("EditVersion: %v", err)
	}
	_ = result

	// 基础列：published_at 非零且已刷新（不早于测试开始）、published_by = 当前登录人
	var row publishTraceDoc
	if err := db.First(&row, "ulid = ?", "v-old").Error; err != nil {
		t.Fatalf("查回落库行: %v", err)
	}
	if row.VersionStatus != string(VersionStatusPublished) {
		t.Fatalf("version_status = %q, want published", row.VersionStatus)
	}
	if row.PublishedAt == nil || row.PublishedAt.IsZero() {
		t.Fatal("REQ §六·1: published_at 必须非零")
	}
	if row.PublishedAt.Before(deadline) {
		t.Errorf("published_at 应为本次发布时刻（>=%v），实际 %v（疑似沿用了旧值）",
			deadline, *row.PublishedAt)
	}
	if row.PublishedBy != "user-A" {
		t.Errorf("published_by = %q, want user-A（REQ §六·1）", row.PublishedBy)
	}

	// 发布历史：多一条，via=edit-version
	hist := w.all()
	if len(hist) != 1 {
		t.Fatalf("REQ §六·1: 发布历史应为 1 条，实际 %d 条: %+v", len(hist), hist)
	}
	if hist[0].Via != string(PublishViaEditVersion) {
		t.Errorf("发布历史 via = %q, want %q", hist[0].Via, PublishViaEditVersion)
	}
	if hist[0].EntityID != "v-old" || hist[0].EntityCode != "CODE1" {
		t.Errorf("发布历史应带实体与版本族信息，实际 %+v", hist[0])
	}
	if hist[0].OperatorULID != "user-A" {
		t.Errorf("发布历史 operator_ulid = %q, want user-A", hist[0].OperatorULID)
	}
	if hist[0].HistoryULID == "" || hist[0].PublishedAt == 0 {
		t.Errorf("发布历史应含 ULID 与发布时间，实际 %+v", hist[0])
	}

	// 方案 C：回调恰好一次，via=edit-version
	if got := *vias; len(got) != 1 || got[0] != PublishViaEditVersion {
		t.Errorf("OnPublished 应恰好触发 1 次且 via=edit-version，实际 %v", got)
	}
}

// ============================================================
// ② 反向：published → deprecated（下线）不得留痕
// ============================================================

func TestPublishTraceEditVersionDeprecateWritesNothing(t *testing.T) {
	svc, db, w, vias := newPublishTraceSvc(t)
	ctx := traceCtx("user-B")

	orig := time.Now().Add(-48 * time.Hour)
	row := &publishTraceDoc{
		ULID: "v-live", Code: "CODE2", VersionCode: "v2.0",
		VersionStatus: string(VersionStatusPublished), IsCurrent: 1,
		PublishedAt: &orig, PublishedBy: "original-publisher",
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("seed published: %v", err)
	}

	if _, err := svc.EditVersion(ctx, "v-live", map[string]any{
		"version_status": string(VersionStatusDeprecated),
	}); err != nil {
		t.Fatalf("EditVersion: %v", err)
	}

	var got publishTraceDoc
	if err := db.First(&got, "ulid = ?", "v-live").Error; err != nil {
		t.Fatalf("查回落库行: %v", err)
	}
	if got.VersionStatus != string(VersionStatusDeprecated) {
		t.Fatalf("version_status = %q, want deprecated", got.VersionStatus)
	}
	// REQ §六·2：下线不得写 published_at，也不得产生发布记录
	if got.PublishedAt == nil || !got.PublishedAt.Equal(orig) {
		t.Errorf("REQ §六·2: 下线不得改写 published_at，want %v，实际 %v", orig, got.PublishedAt)
	}
	if got.PublishedBy != "original-publisher" {
		t.Errorf("REQ §六·2: 下线不得改写 published_by，实际 %q", got.PublishedBy)
	}
	if n := len(w.all()); n != 0 {
		t.Errorf("REQ §六·2: 下线不得产生发布历史，实际 %d 条", n)
	}
	if got := *vias; len(got) != 0 {
		t.Errorf("REQ §六·2: 下线不得触发 OnPublished，实际 %v", got)
	}
}

// ============================================================
// ③ 反向：只改备注、状态不变时两项均不变
// ============================================================

func TestPublishTraceEditVersionRemarkOnlyWritesNothing(t *testing.T) {
	cases := []struct {
		name   string
		status VersionStatus
	}{
		{"published 只改备注", VersionStatusPublished},
		{"deprecated 只改备注", VersionStatusDeprecated},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, db, w, vias := newPublishTraceSvc(t)
			ctx := traceCtx("user-C")

			orig := time.Now().Add(-12 * time.Hour)
			row := &publishTraceDoc{
				ULID: "v-1", Code: "CODE3", VersionCode: "v1.0",
				VersionStatus: string(c.status),
				PublishedAt:   &orig, PublishedBy: "original-publisher",
			}
			if err := db.Create(row).Error; err != nil {
				t.Fatalf("seed: %v", err)
			}

			if _, err := svc.EditVersion(ctx, "v-1", map[string]any{
				"version_remark": "只改备注",
			}); err != nil {
				t.Fatalf("EditVersion: %v", err)
			}

			var got publishTraceDoc
			if err := db.First(&got, "ulid = ?", "v-1").Error; err != nil {
				t.Fatalf("查回: %v", err)
			}
			if got.VersionRemark != "只改备注" {
				t.Fatalf("备注应已更新，实际 %q", got.VersionRemark)
			}
			if got.PublishedAt == nil || !got.PublishedAt.Equal(orig) {
				t.Errorf("REQ §六·3: 状态不变不得改写 published_at，实际 %v", got.PublishedAt)
			}
			if got.PublishedBy != "original-publisher" {
				t.Errorf("REQ §六·3: 状态不变不得改写 published_by，实际 %q", got.PublishedBy)
			}
			if n := len(w.all()); n != 0 {
				t.Errorf("REQ §六·3: 不得产生发布历史，实际 %d 条", n)
			}
			if got := *vias; len(got) != 0 {
				t.Errorf("REQ §六·3: 不得触发 OnPublished，实际 %v", got)
			}
		})
	}
}

// ============================================================
// ④ activate 路径既有留痕行为不变（回归保护）
// ============================================================

func TestPublishTraceActivateKeepsExistingBehaviour(t *testing.T) {
	svc, db, w, vias := newPublishTraceSvc(t)
	ctx := traceCtx("user-D")

	draft := &publishTraceDoc{
		ULID: "v-draft", Code: "CODE4", VersionCode: "v1.0",
		VersionStatus: string(VersionStatusDraft), IsCurrent: 1,
	}
	if err := db.Create(draft).Error; err != nil {
		t.Fatalf("seed draft: %v", err)
	}

	if err := svc.Activate(ctx, "v-draft"); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	var got publishTraceDoc
	if err := db.First(&got, "ulid = ?", "v-draft").Error; err != nil {
		t.Fatalf("查回: %v", err)
	}
	if got.VersionStatus != string(VersionStatusPublished) {
		t.Fatalf("activate 后 version_status = %q, want published", got.VersionStatus)
	}
	if got.PublishedAt == nil || got.PublishedAt.IsZero() {
		t.Error("REQ §六·4: activate 必须写 published_at（原行为）")
	}
	if got.PublishedBy != "user-D" {
		t.Errorf("REQ §六·4: activate 必须写 published_by（原行为），实际 %q", got.PublishedBy)
	}
	hist := w.all()
	if len(hist) != 1 || hist[0].Via != string(PublishViaActivate) {
		t.Errorf("REQ §六·4/5: activate 应产生 1 条 via=activate 的发布历史，实际 %+v", hist)
	}
	if v := *vias; len(v) != 1 || v[0] != PublishViaActivate {
		t.Errorf("REQ §六·5: OnPublished 应恰好触发 1 次且 via=activate，实际 %v", v)
	}
}

// ============================================================
// ⑤ 四条路径各触发一次且仅一次 OnPublished
// ============================================================

func TestPublishTraceOnPublishedFiresOncePerPath(t *testing.T) {
	svc, db, w, vias := newPublishTraceSvc(t)

	// --- 路径 1/2：create「保存并发布」---
	// SupportsDraft=true 且显式传 published：应走 create 一次，且**不**额外走 update/activate。
	created, err := svc.Create(traceCtx("u1"), []CrudRequest[*publishTraceDoc]{
		&publishTraceReq[*publishTraceDoc]{data: map[string]any{
			"code": "CODE5", "name": "n", "version_status": string(VersionStatusPublished),
		}},
	})
	if err != nil {
		t.Fatalf("Create(published): %v", err)
	}
	firstID := (*created[0]).ULID

	if v := *vias; len(v) != 1 || v[0] != PublishViaCreate {
		t.Fatalf("REQ §六·5: create 保存并发布应恰好触发 1 次 via=create，实际 %v", v)
	}
	var createdRow publishTraceDoc
	if err := db.First(&createdRow, "ulid = ?", firstID).Error; err != nil {
		t.Fatalf("查回 create 行: %v", err)
	}
	if createdRow.PublishedAt == nil || createdRow.PublishedAt.IsZero() {
		t.Error("create 保存并发布必须写 published_at")
	}
	if createdRow.PublishedBy != "u1" {
		t.Errorf("create 的 published_by = %q, want u1", createdRow.PublishedBy)
	}
	if n := len(w.all()); n != 1 {
		t.Errorf("create 应产生 1 条发布历史，实际 %d 条", n)
	}

	// --- 路径 3：update 落成 published（草稿 → 发布，走版本化 update）---
	// 先把该版本行改成 draft，再通过 Update 提交 published。
	if err := db.Model(&publishTraceDoc{}).Where("ulid = ?", firstID).
		Update("version_status", string(VersionStatusDraft)).Error; err != nil {
		t.Fatalf("改为 draft: %v", err)
	}
	before := len(*vias)
	if _, err := svc.Update(traceCtx("u2"), firstID, &publishTraceReq[*publishTraceDoc]{
		data: map[string]any{"version_status": string(VersionStatusPublished)},
	}); err != nil {
		t.Fatalf("Update(published): %v", err)
	}
	got := (*vias)[before:]
	if len(got) != 1 || got[0] != PublishViaUpdate {
		t.Fatalf("REQ §六·5: update 落成 published 应恰好触发 1 次 via=update，实际 %v", got)
	}

	// --- 路径 4：activate 发布草稿 ---
	draft := &publishTraceDoc{
		ULID: "v-act", Code: "CODE8", VersionCode: "v1.0",
		VersionStatus: string(VersionStatusDraft), IsCurrent: 1,
	}
	if err := db.Create(draft).Error; err != nil {
		t.Fatalf("seed activate draft: %v", err)
	}
	before = len(*vias)
	if err := svc.Activate(traceCtx("u4"), "v-act"); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	got = (*vias)[before:]
	if len(got) != 1 || got[0] != PublishViaActivate {
		t.Fatalf("REQ §六·5: activate 应恰好触发 1 次 via=activate，实际 %v", got)
	}

	// --- 路径 5：edit-version 复活 deprecated ---
	seedDeprecated(t, db, "v-resurrect", "CODE6")
	before = len(*vias)
	if _, err := svc.EditVersion(traceCtx("u3"), "v-resurrect", map[string]any{
		"version_status": string(VersionStatusPublished),
	}); err != nil {
		t.Fatalf("EditVersion: %v", err)
	}
	got = (*vias)[before:]
	if len(got) != 1 || got[0] != PublishViaEditVersion {
		t.Fatalf("REQ §六·5: edit-version 复活应恰好触发 1 次 via=edit-version，实际 %v", got)
	}

	// 汇总：4 条路径各 1 次，互不重复
	all := *vias
	if len(all) != 4 {
		t.Fatalf("REQ §六·5: 4 条路径应共触发 4 次，实际 %d 次: %v", len(all), all)
	}
	seen := map[PublishVia]int{}
	for _, v := range all {
		seen[v]++
	}
	for _, wantVia := range []PublishVia{
		PublishViaCreate, PublishViaUpdate, PublishViaActivate, PublishViaEditVersion,
	} {
		if seen[wantVia] != 1 {
			t.Errorf("via=%s 应恰好 1 次，实际 %d 次（全量 %v）", wantVia, seen[wantVia], all)
		}
	}
}

// ============================================================
// 补充：判定函数与来源标记的纯函数语义固化
// ============================================================

func TestPublishTraceStatusBecomesPublished(t *testing.T) {
	svc := &GenericService[*publishTraceDoc]{
		config: Config[*publishTraceDoc]{
			VersionMode: true,
			VersionFields: &VersionFieldMapping{
				ULIDField: "ULID", CodeField: "Code", VersionField: "VersionCode",
				CurrentField: "IsCurrent", StatusField: "VersionStatus",
				PublishedAtField: "PublishedAt", PublishedByField: "PublishedBy",
			},
		},
	}
	cases := []struct {
		old, new string
		want     bool
	}{
		{"deprecated", "published", true},  // 复活已废弃版本 = 需要留痕的新发布（REQ §五）
		{"", "published", true},            // create 直接发布
		{"draft", "published", true},       // 草稿发布（activate 路径判定同源）
		{"published", "published", false},  // 原地不改，不算重新发布
		{"published", "deprecated", false}, // 下线不留痕（REQ §六·2）
		{"deprecated", "abolished", false}, // 归档不留痕
		{"abolished", "draft", false},      // 恢复草稿不留痕
	}
	for _, c := range cases {
		if got := svc.statusBecomesPublished(c.old, c.new); got != c.want {
			t.Errorf("statusBecomesPublished(%q, %q) = %v, want %v", c.old, c.new, got, c.want)
		}
	}

	// 状态字段未配置 → 无法判定，恒 false
	noStatus := &GenericService[*publishTraceDoc]{
		config: Config[*publishTraceDoc]{
			VersionMode:   true,
			VersionFields: &VersionFieldMapping{ULIDField: "ULID"},
		},
	}
	if noStatus.statusBecomesPublished("deprecated", "published") {
		t.Error("状态字段未配置时应返回 false（无法判定）")
	}
	// 非版本化实体恒 false
	plain := &GenericService[*publishTraceDoc]{config: Config[*publishTraceDoc]{}}
	if plain.statusBecomesPublished("deprecated", "published") {
		t.Error("非版本化实体应返回 false")
	}
}

func TestPublishTraceIsValidPublishVia(t *testing.T) {
	valid := []PublishVia{
		PublishViaCreate, PublishViaUpdate, PublishViaActivate, PublishViaEditVersion,
	}
	for _, v := range valid {
		if !IsValidPublishVia(v) {
			t.Errorf("%q 应为合法来源", v)
		}
	}
	// 字面值固化：REQ 明确要求 via ∈ {"create","update","activate","edit-version"}
	want := []string{"create", "update", "activate", "edit-version"}
	got := make([]string, len(valid))
	for i, v := range valid {
		got[i] = string(v)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("来源字面值 = %v, want %v（REQ §四·C 契约）", got, want)
	}
	for _, bad := range []PublishVia{"", "unknown", "publish", "ACTIVATE"} {
		if IsValidPublishVia(bad) {
			t.Errorf("%q 不应为合法来源", bad)
		}
	}
}

// TestPublishTraceNoStatusColumnNoHistoryWithoutBaseColumns 配置缺口语义：
// 两个发布痕迹基础列都没配置时，仍触发回调与发布历史，并打 Warn 提示配置缺失。
func TestPublishTraceMissingBaseColumnsStillTraces(t *testing.T) {
	db := openBug069DB(t, &publishTraceDoc{})
	svc := NewGenericService[*publishTraceDoc](repository.NewCRUDWithDB[*publishTraceDoc](db), Config[*publishTraceDoc]{
		EntityName:  "no_trace_cols",
		VersionMode: true,
		VersionFields: &VersionFieldMapping{
			ULIDField: "ULID", CodeField: "Code", VersionField: "VersionCode",
			CurrentField: "IsCurrent", StatusField: "VersionStatus",
			// PublishedAtField / PublishedByField 故意都不配置
		},
	})
	w := &memPublishHistoryWriter{}
	svc.SetPublishHistoryWriter(w)
	seedDeprecated(t, db, "v-x", "CODE7")

	var fired int
	svc.SetOnPublished(func(context.Context, any, **publishTraceDoc, PublishVia) error {
		fired++
		return nil
	})

	if _, err := svc.EditVersion(traceCtx("u"), "v-x", map[string]any{
		"version_status": string(VersionStatusPublished),
	}); err != nil {
		t.Fatalf("EditVersion: %v", err)
	}
	// 基础列无处安放，但历史与回调仍应发生（不让配置缺口吞掉业务事实）
	if len(w.all()) != 1 {
		t.Errorf("配置缺口下仍应写 1 条发布历史，实际 %d 条", len(w.all()))
	}
	if fired != 1 {
		t.Errorf("配置缺口下仍应触发 1 次回调，实际 %d 次", fired)
	}
}
