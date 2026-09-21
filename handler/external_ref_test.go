package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/Huey1979/gocrux/constants"
	errs "github.com/Huey1979/gocrux/errors"
	"github.com/Huey1979/gocrux/repository"
	"github.com/Huey1979/gocrux/service"
)

// ============================================================
// 外部引用解析回归测试（应用方 §19.4 / §20.3 / §20.5）
//
// 覆盖：
//  1 published 与 current 的**分叉**（存在编辑中草稿时二者必须不同）
//  2 陈旧检测（提交的 ULID ≠ 按 code 解析出的权威 ULID → ErrExternalRefStale）
//  3 默认模式推导（有 ULID → snapshot；仅 code → 版本化 published / 非版本化 current）
//  4 不可作为引用目标的情形（不存在 / 已软删）
//  5 参数与目标 Handler 的错误分级（InvalidParam / TargetMissing）
//  6 mapServiceError 的错误码映射
// ============================================================

// -------- 测试实体 --------

// refTargetDoc 版本化目标（模拟 heims 的表单/流程配置：code + 版本 + 状态）。
type refTargetDoc struct {
	ULID          string     `gorm:"column:ulid;primaryKey;size:26" json:"ulid"`
	Code          string     `gorm:"column:code;size:64" json:"code"`
	Name          string     `gorm:"column:name;size:100" json:"name"`
	VersionCode   string     `gorm:"column:version_code;size:20" json:"version_code"`
	VersionStatus string     `gorm:"column:version_status;size:20" json:"version_status"`
	IsCurrent     int8       `gorm:"column:is_current;default:0" json:"is_current"`
	PublishedAt   *time.Time `gorm:"column:published_at" json:"published_at"`
	IsDeleted     int8       `gorm:"column:is_deleted;default:0" json:"-"`
}

func (d *refTargetDoc) SetDefaults()             {}
func (d *refTargetDoc) SetCreatedAt(_ time.Time) {}
func (d *refTargetDoc) SetCreatedBy(string)      {}
func (d *refTargetDoc) SetUpdatedAt(_ time.Time) {}
func (d *refTargetDoc) SetUpdatedBy(string)      {}
func (d *refTargetDoc) SupportsDraft() bool      { return true }
func (d *refTargetDoc) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *refTargetDoc) PKField() string          { return "ulid" }
func (d *refTargetDoc) SelfFKField() string      { return "" }

// refPlainDoc 非版本化目标（模拟普通业务实体，只有主键 + code）。
type refPlainDoc struct {
	ULID      string `gorm:"column:ulid;primaryKey;size:26" json:"ulid"`
	Code      string `gorm:"column:code;size:64" json:"code"`
	Name      string `gorm:"column:name;size:100" json:"name"`
	IsDeleted int8   `gorm:"column:is_deleted;default:0" json:"-"`
}

func (d *refPlainDoc) SetDefaults()             {}
func (d *refPlainDoc) SetCreatedAt(_ time.Time) {}
func (d *refPlainDoc) SetCreatedBy(string)      {}
func (d *refPlainDoc) SetUpdatedAt(_ time.Time) {}
func (d *refPlainDoc) SetUpdatedBy(string)      {}
func (d *refPlainDoc) SupportsDraft() bool      { return false }
func (d *refPlainDoc) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *refPlainDoc) PKField() string          { return "ulid" }
func (d *refPlainDoc) SelfFKField() string      { return "" }

// -------- 测试装置 --------

const (
	refTargetName = "ref_target"
	refPlainName  = "ref_plain"
)

// openExternalRefDB 打开共享内存库并清表（sqlite file::memory:?cache=shared
// 跨用例保留数据，必须每次清干净，否则断言互相污染）。
func openExternalRefDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sqlDB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&refTargetDoc{}, &refPlainDoc{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	for _, m := range []any{&refTargetDoc{}, &refPlainDoc{}} {
		if err := db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(m).Error; err != nil {
			t.Fatalf("清表失败: %v", err)
		}
	}
	return db
}

// buildExternalRefRegistry 构造「版本化目标 + 非版本化目标」的注册表。
func buildExternalRefRegistry(t *testing.T, db *gorm.DB) *HandlerRegistry {
	t.Helper()

	versioned := service.NewGenericService[*refTargetDoc](
		repository.NewCRUDWithDB[*refTargetDoc](db),
		service.Config[*refTargetDoc]{
			EntityName:  refTargetName,
			VersionMode: true,
			VersionFields: &service.VersionFieldMapping{
				ULIDField:        "ULID",
				CodeField:        "Code",
				VersionField:     "VersionCode",
				CurrentField:     "IsCurrent",
				StatusField:      "VersionStatus",
				PublishedAtField: "PublishedAt",
			},
		})
	plain := service.NewGenericService[*refPlainDoc](
		repository.NewCRUDWithDB[*refPlainDoc](db),
		service.Config[*refPlainDoc]{EntityName: refPlainName})

	reg := NewHandlerRegistry()
	reg.Register(refTargetName, NewGenericHandlerWithSvc[*refTargetDoc](
		versioned, refTargetName, HandlerConfig[*refTargetDoc]{PathPrefix: "/ref/target"}))
	reg.Register(refPlainName, NewGenericHandlerWithSvc[*refPlainDoc](
		plain, refPlainName, HandlerConfig[*refPlainDoc]{PathPrefix: "/ref/plain"}))
	return reg
}

// seedVersioned 写入一条版本化目标记录。
func seedVersioned(t *testing.T, db *gorm.DB, ulid, code, status string, current int8, vcode string) {
	t.Helper()
	published := time.Now()
	if status != string(service.VersionStatusPublished) {
		published = time.Time{}
	}
	row := &refTargetDoc{
		ULID: ulid, Code: code, Name: code + "-" + vcode,
		VersionCode: vcode, VersionStatus: status, IsCurrent: current,
	}
	if !published.IsZero() {
		row.PublishedAt = &published
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("seed versioned %s: %v", ulid, err)
	}
}

// -------- 1 published / current 的分叉 --------

func TestExternalRefPublishedVsCurrentDiverge(t *testing.T) {
	db := openExternalRefDB(t)
	reg := buildExternalRefRegistry(t, db)
	ctx := context.Background()

	// 线上生效版本 v1.0（published），以及编辑中的草稿 v2.0（is_current=1）。
	seedVersioned(t, db, "LIVE1", "FX", string(service.VersionStatusPublished), 0, "v1.0")
	seedVersioned(t, db, "DRAFT1", "FX", string(service.VersionStatusDraft), 1, "v2.0")

	pub, err := ResolveExternalRef(ctx, reg, ExternalRefLookup{
		HandlerName: refTargetName, Code: "FX", Mode: ExternalRefModePublished,
	})
	if err != nil {
		t.Fatalf("published 解析失败: %v", err)
	}
	if pub.ULID != "LIVE1" {
		t.Fatalf("published 应指向线上生效版本 LIVE1: got=%s", pub.ULID)
	}
	if pub.VersionStatus != string(service.VersionStatusPublished) {
		t.Fatalf("VersionStatus 应为 published: got=%q", pub.VersionStatus)
	}
	if pub.Code != "FX" || pub.VersionCode != "v1.0" {
		t.Fatalf("权威身份不完整: code=%q version_code=%q", pub.Code, pub.VersionCode)
	}
	if pub.Record["name"] != "FX-v1.0" {
		t.Fatalf("Record 应为权威记录的 JSON 形态: %v", pub.Record)
	}

	cur, err := ResolveExternalRef(ctx, reg, ExternalRefLookup{
		HandlerName: refTargetName, Code: "FX", Mode: ExternalRefModeCurrent,
	})
	if err != nil {
		t.Fatalf("current 解析失败: %v", err)
	}
	if cur.ULID != "DRAFT1" {
		t.Fatalf("current 应指向 is_current=1 的草稿 DRAFT1: got=%s", cur.ULID)
	}
	if cur.ULID == pub.ULID {
		t.Fatal("存在编辑中草稿时 published 与 current 必须分叉（否则 GetByCode 可直接替代 GetPublishedByCode）")
	}
}

// -------- 2 陈旧检测 --------

func TestExternalRefStaleWhenULIDOutdated(t *testing.T) {
	db := openExternalRefDB(t)
	reg := buildExternalRefRegistry(t, db)
	ctx := context.Background()
	seedVersioned(t, db, "LIVE2", "FY", string(service.VersionStatusPublished), 1, "v1.0")

	// 提交了正确的权威 ULID → 通过
	if _, err := ResolveExternalRef(ctx, reg, ExternalRefLookup{
		HandlerName: refTargetName, Code: "FY", ULID: "LIVE2",
		Mode: ExternalRefModePublished,
	}); err != nil {
		t.Fatalf("权威 ULID 应通过: %v", err)
	}

	// 提交了旧版本的 ULID → 必须报「版本已变化」，不得静默改成新版本
	_, err := ResolveExternalRef(ctx, reg, ExternalRefLookup{
		HandlerName: refTargetName, Code: "FY", ULID: "OLDVERSION",
		Mode: ExternalRefModePublished,
	})
	if err == nil {
		t.Fatal("ULID 与权威版本不一致时必须报错（否则引用旧版本会静默落库）")
	}
	if !errors.Is(err, errs.ErrExternalRefStale) {
		t.Fatalf("应为 ErrExternalRefStale: %v", err)
	}
}

// -------- 3 默认模式推导 --------

func TestExternalRefDefaultModes(t *testing.T) {
	db := openExternalRefDB(t)
	reg := buildExternalRefRegistry(t, db)
	ctx := context.Background()
	seedVersioned(t, db, "LIVE3", "FZ", string(service.VersionStatusPublished), 1, "v1.0")
	if err := db.Create(&refPlainDoc{ULID: "P1", Code: "PC1", Name: "普通"}).Error; err != nil {
		t.Fatalf("seed plain: %v", err)
	}

	// 只给 code + 版本化目标 → published
	got, err := ResolveExternalRef(ctx, reg, ExternalRefLookup{HandlerName: refTargetName, Code: "FZ"})
	if err != nil || got.ULID != "LIVE3" {
		t.Fatalf("版本化 + 只有 code 应默认 published → LIVE3: got=%v err=%v", got, err)
	}

	// 给了 ULID → snapshot（固定该版本）
	got, err = ResolveExternalRef(ctx, reg, ExternalRefLookup{HandlerName: refTargetName, ULID: "LIVE3"})
	if err != nil || got.ULID != "LIVE3" {
		t.Fatalf("给了 ULID 应默认 snapshot: got=%v err=%v", got, err)
	}

	// 只给 code + 非版本化目标 → current（退化为按 code 等值查询）
	got, err = ResolveExternalRef(ctx, reg, ExternalRefLookup{HandlerName: refPlainName, Code: "PC1"})
	if err != nil || got.ULID != "P1" {
		t.Fatalf("非版本化 + code 应解析到 P1: got=%v err=%v", got, err)
	}
	if got.VersionStatus != "" || got.VersionCode != "" {
		t.Fatalf("非版本化目标不应有版本字段: %+v", got)
	}
}

// §23.3 回归：**Mode 留空**时「Code + ULID」必须走 published + 陈旧检测，
// 不得降级成 snapshot。
//
// 这是应用方复核发现的实现/文档不一致：早期实现「只要 ULID 非空 → snapshot」，
// 于是前端拿旧 ULID + 新 code 拼接就能把引用悄悄固定到过期版本，
// 与 §20.3 第 3 条「版本已变化，请重新选择」直接冲突。
func TestExternalRefDefaultModeCodeWithULID(t *testing.T) {
	db := openExternalRefDB(t)
	reg := buildExternalRefRegistry(t, db)
	ctx := context.Background()

	// 线上生效版本 v2.0 + 已废弃的旧版本 v1.0（同一 code）
	seedVersioned(t, db, "LIVE9", "FQ", string(service.VersionStatusPublished), 1, "v2.0")
	seedVersioned(t, db, "OLD9", "FQ", string(service.VersionStatusDeprecated), 0, "v1.0")

	// ① Code + ULID（权威）且 Mode 留空 → published 语义：通过，且回传权威身份
	got, err := ResolveExternalRef(ctx, reg, ExternalRefLookup{
		HandlerName: refTargetName, Code: "FQ", ULID: "LIVE9",
	})
	if err != nil {
		t.Fatalf("Code+ULID 且权威一致时应通过: %v", err)
	}
	if got.VersionStatus != string(service.VersionStatusPublished) {
		t.Fatalf("Mode 留空的 Code+ULID 应走 published（陈旧检测），实际 VersionStatus=%q",
			got.VersionStatus)
	}

	// ② Code + 旧 ULID 且 Mode 留空 → **必须报「版本已变化」**
	// （若被降级成 snapshot，这里会静默成功 —— 本用例正是为钉住这一点）
	_, err = ResolveExternalRef(ctx, reg, ExternalRefLookup{
		HandlerName: refTargetName, Code: "FQ", ULID: "OLD9",
	})
	if !errors.Is(err, errs.ErrExternalRefStale) {
		t.Fatalf("Code + 旧 ULID（Mode 留空）应报 ErrExternalRefStale，实际: %v", err)
	}

	// ③ 确实需要「固定旧版本」时，显式传 snapshot 仍然可用
	got, err = ResolveExternalRef(ctx, reg, ExternalRefLookup{
		HandlerName: refTargetName, Code: "FQ", ULID: "OLD9", Mode: ExternalRefModeSnapshot,
	})
	if err != nil {
		t.Fatalf("显式 snapshot 应允许固定旧版本: %v", err)
	}
	if got.ULID != "OLD9" || got.VersionCode != "v1.0" {
		t.Fatalf("snapshot 应固定到 OLD9(v1.0): %+v", got)
	}

	// ④ 非版本化目标：Code + ULID 一致 → 通过（published 退化为按 code 查询 + 一致性校验）
	if err := db.Create(&refPlainDoc{ULID: "P9", Code: "PC9", Name: "普通"}).Error; err != nil {
		t.Fatalf("seed plain: %v", err)
	}
	if _, err := ResolveExternalRef(ctx, reg, ExternalRefLookup{
		HandlerName: refPlainName, Code: "PC9", ULID: "P9",
	}); err != nil {
		t.Fatalf("非版本化目标 Code+ULID 一致时应通过: %v", err)
	}
	if _, err := ResolveExternalRef(ctx, reg, ExternalRefLookup{
		HandlerName: refPlainName, Code: "PC9", ULID: "P_MISMATCH",
	}); !errors.Is(err, errs.ErrExternalRefStale) {
		t.Fatalf("非版本化目标 Code 与 ULID 不一致应报 Stale: %v", err)
	}
}

// 只有草稿、尚无 published → published 模式必须报不存在（不能回退到草稿）。
func TestExternalRefPublishedMissingWhenDraftOnly(t *testing.T) {
	db := openExternalRefDB(t)
	reg := buildExternalRefRegistry(t, db)
	ctx := context.Background()
	seedVersioned(t, db, "DRAFTONLY", "FW", string(service.VersionStatusDraft), 1, "v0.1")

	_, err := ResolveExternalRef(ctx, reg, ExternalRefLookup{
		HandlerName: refTargetName, Code: "FW", Mode: ExternalRefModePublished,
	})
	if !errors.Is(err, errs.ErrExternalRefNotFound) {
		t.Fatalf("无 published 版本时 published 模式应报 NotFound: %v", err)
	}
	// current 模式仍能取到草稿（语义不同，各自成立）
	if got, err := ResolveExternalRef(ctx, reg, ExternalRefLookup{
		HandlerName: refTargetName, Code: "FW", Mode: ExternalRefModeCurrent,
	}); err != nil || got.ULID != "DRAFTONLY" {
		t.Fatalf("current 模式应取到草稿: got=%v err=%v", got, err)
	}
}

// -------- 4 不可作为引用目标 --------

func TestExternalRefRejectsMissingAndSoftDeleted(t *testing.T) {
	db := openExternalRefDB(t)
	reg := buildExternalRefRegistry(t, db)
	ctx := context.Background()
	seedVersioned(t, db, "DEL1", "FD", string(service.VersionStatusPublished), 1, "v1.0")
	if err := db.Model(&refTargetDoc{}).Where("ulid = ?", "DEL1").
		Update("is_deleted", 1).Error; err != nil {
		t.Fatalf("软删: %v", err)
	}

	// 已软删：读路径本身不过滤（BUG-069），但引用目标必须显式拒绝
	_, err := ResolveExternalRef(ctx, reg, ExternalRefLookup{HandlerName: refTargetName, ULID: "DEL1"})
	if !errors.Is(err, errs.ErrExternalRefNotFound) {
		t.Fatalf("已软删记录不可作为引用目标: %v", err)
	}

	// 不存在（底层「记录不存在」被归一，但原始错误链保留 —— 应用可同时按两种哨兵判定）
	_, err = ResolveExternalRef(ctx, reg, ExternalRefLookup{HandlerName: refTargetName, ULID: "NOTEXIST"})
	if !errors.Is(err, errs.ErrExternalRefNotFound) {
		t.Fatalf("不存在的 ULID 应报 NotFound: %v", err)
	}
	if !errors.Is(err, errs.ErrRecordNotFound) {
		t.Fatalf("归一后应保留 ErrRecordNotFound 链: %v", err)
	}
	_, err = ResolveExternalRef(ctx, reg, ExternalRefLookup{HandlerName: refTargetName, Code: "NOPE"})
	if !errors.Is(err, errs.ErrExternalRefNotFound) {
		t.Fatalf("不存在的 code 应报 NotFound: %v", err)
	}
}

// snapshot 模式同时给 code 时，code 必须与固定版本一致（防「A 的 ULID + B 的 code」拼接绕过）。
func TestExternalRefSnapshotCodeMustMatch(t *testing.T) {
	db := openExternalRefDB(t)
	reg := buildExternalRefRegistry(t, db)
	ctx := context.Background()
	seedVersioned(t, db, "SNAP1", "FS", string(service.VersionStatusPublished), 1, "v1.0")

	if _, err := ResolveExternalRef(ctx, reg, ExternalRefLookup{
		HandlerName: refTargetName, ULID: "SNAP1", Code: "FS", Mode: ExternalRefModeSnapshot,
	}); err != nil {
		t.Fatalf("code 与 ULID 一致时应通过: %v", err)
	}

	_, err := ResolveExternalRef(ctx, reg, ExternalRefLookup{
		HandlerName: refTargetName, ULID: "SNAP1", Code: "OTHER", Mode: ExternalRefModeSnapshot,
	})
	if !errors.Is(err, errs.ErrExternalRefInvalidParam) {
		t.Fatalf("code 与固定版本不一致应报参数错误: %v", err)
	}
}

// -------- 5 参数 / 目标 Handler 错误分级 --------

func TestExternalRefParamAndTargetErrors(t *testing.T) {
	db := openExternalRefDB(t)
	reg := buildExternalRefRegistry(t, db)
	ctx := context.Background()

	cases := []struct {
		name    string
		reg     *HandlerRegistry
		in      ExternalRefLookup
		wantErr error
	}{
		{"缺 HandlerName", reg, ExternalRefLookup{Code: "X"}, errs.ErrExternalRefInvalidParam},
		{"code 与 ulid 都缺", reg, ExternalRefLookup{HandlerName: refTargetName}, errs.ErrExternalRefInvalidParam},
		{"未知 Mode", reg, ExternalRefLookup{HandlerName: refTargetName, Code: "X", Mode: "weird"}, errs.ErrExternalRefInvalidParam},
		{"snapshot 缺 ulid", reg, ExternalRefLookup{HandlerName: refTargetName, Code: "X", Mode: ExternalRefModeSnapshot}, errs.ErrExternalRefInvalidParam},
		{"published 缺 code", reg, ExternalRefLookup{HandlerName: refTargetName, ULID: "U", Mode: ExternalRefModePublished}, errs.ErrExternalRefInvalidParam},
		{"Handler 未注册", reg, ExternalRefLookup{HandlerName: "not_registered", Code: "X"}, errs.ErrExternalRefTargetMissing},
		{"注册表未注入", nil, ExternalRefLookup{HandlerName: refTargetName, Code: "X"}, errs.ErrExternalRefTargetMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveExternalRef(ctx, tc.reg, tc.in)
			if err == nil {
				t.Fatal("应报错")
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("应为 %v: %v", tc.wantErr, err)
			}
		})
	}
}

// -------- 6 错误码映射 --------

func TestExternalRefErrorCodeMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want constants.BusinessCode
	}{
		{"NotFound → 404", errs.ErrExternalRefNotFound, constants.CodeNotFound},
		{"Stale → 409", errs.ErrExternalRefStale, constants.CodeConflict},
		{"TargetMissing → 500", errs.ErrExternalRefTargetMissing, constants.CodeInternalError},
		{"InvalidParam → 4001", errs.ErrExternalRefInvalidParam, constants.CodeParamError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mapServiceError(tc.err); got != tc.want {
				t.Fatalf("mapServiceError = %d, want %d", got, tc.want)
			}
		})
	}
}
