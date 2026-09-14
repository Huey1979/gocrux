// BUG-074 回归测试：ErrDuplicateCode 文案不应写死「form_code / 表单」。
//
// 缺陷要点：该哨兵被**全部版本化实体**共用（form / flow / notification_template /
// container / bi_chart / site / site_menu / role），但文案里写死了 form_code 与
// 「表单」——heims 在**通知模板**页新建重码模板时，toast 让用户去改 form_code，
// 把用户引到完全不相干的模块。
//
// 修复：① 文案中性化；② VersionFieldMapping 新增可选 DuplicateCodeMsg 供实体覆写。
//
// 本文件覆盖：① 中性文案不含 form_code / 表单；② 未配置覆写时用中性文案且
// errors.Is(err, ErrDuplicateCode) 仍成立；③ 配置覆写时使用专属文案且哨兵判定不破；
// ④ 真实走 _doCreate 的版本化重码分支，断言用户可见 msg。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Huey1979/gocrux/repository"

	errs "github.com/Huey1979/gocrux/errors"
)

// bug074Doc 版本化实体（M = *bug074Doc）。
type bug074Doc struct {
	ULID          string `gorm:"column:ulid;primaryKey;size:26" json:"ulid"`
	Code          string `gorm:"column:code;size:64" json:"code"`
	Name          string `gorm:"column:name;size:100" json:"name"`
	VersionCode   string `gorm:"column:version_code;size:20" json:"version_code"`
	VersionStatus string `gorm:"column:version_status;size:20" json:"version_status"`
	IsCurrent     int8   `gorm:"column:is_current;default:0" json:"is_current"`
	ParentULID    string `gorm:"column:parent_ulid;size:26" json:"parent_ulid"`
	IsDeleted     int8   `gorm:"column:is_deleted;default:0" json:"-"`
}

func (d *bug074Doc) SetDefaults()             {}
func (d *bug074Doc) SetCreatedAt(_ time.Time) {}
func (d *bug074Doc) SetCreatedBy(string)      {}
func (d *bug074Doc) SetUpdatedAt(_ time.Time) {}
func (d *bug074Doc) SetUpdatedBy(string)      {}
func (d *bug074Doc) SupportsDraft() bool      { return false }
func (d *bug074Doc) SetDelete() bool          { d.IsDeleted = 1; return true }
func (d *bug074Doc) PKField() string          { return "ulid" }
func (d *bug074Doc) SelfFKField() string      { return "" }

type bug074Req[M Record] struct{ data map[string]any }

func (r *bug074Req[M]) MergeTo(target *M) error {
	if len(r.data) == 0 {
		return nil
	}
	b, err := json.Marshal(r.data)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, *target)
}
func (r *bug074Req[M]) GetID() any           { return nil }
func (r *bug074Req[M]) Validate() error      { return nil }
func (r *bug074Req[M]) Data() map[string]any { return r.data }

// TestBug074NeutralMessageHasNoFormWording 用例 1：
// 中性文案不得出现 form_code / 表单字样（heims 验收标准 §五 第 1、2 条）。
func TestBug074NeutralMessageHasNoFormWording(t *testing.T) {
	msg := errs.ErrDuplicateCode.Error()
	if strings.Contains(msg, "form_code") {
		t.Errorf("BUG-074: neutral message must not mention form_code, got %q", msg)
	}
	if strings.Contains(msg, "表单") {
		t.Errorf("BUG-074: neutral message must not mention 表单, got %q", msg)
	}
}

// TestBug074DuplicateCodeErrorNeutral 用例 2：未配置覆写时用中性文案，
// 且 errors.Is 哨兵判定不受影响（现有依赖该判定的单测不回归）。
func TestBug074DuplicateCodeErrorNeutral(t *testing.T) {
	err := duplicateCodeError(&VersionFieldMapping{CodeField: "Code"}, "tpl_001")
	if err == nil {
		t.Fatal("duplicateCodeError must return an error")
	}
	if !errors.Is(err, errs.ErrDuplicateCode) {
		t.Error("BUG-074: sentinel match must still hold with the neutral message")
	}
	if strings.Contains(err.Error(), "form_code") || strings.Contains(err.Error(), "表单") {
		t.Errorf("BUG-074: message must be entity-agnostic, got %q", err.Error())
	}
	// 未配置 vf（nil）时也要安全
	if err := duplicateCodeError(nil, "x"); !errors.Is(err, errs.ErrDuplicateCode) {
		t.Error("duplicateCodeError(nil, ...) must still match the sentinel")
	}
}

// TestBug074DuplicateCodeErrorOverride 用例 3：配置 DuplicateCodeMsg 时用专属文案，
// 且 must wrap 哨兵（errors.Is 仍成立）。
func TestBug074DuplicateCodeErrorOverride(t *testing.T) {
	vf := &VersionFieldMapping{
		CodeField:        "TemplateCode",
		DuplicateCodeMsg: "模板编码已存在，请更换 template_code",
	}
	err := duplicateCodeError(vf, "tpl_001")
	if err == nil {
		t.Fatal("duplicateCodeError must return an error")
	}
	if !strings.Contains(err.Error(), "template_code") {
		t.Errorf("override message must be used, got %q", err.Error())
	}
	if strings.Contains(err.Error(), "form_code") {
		t.Errorf("override must fully replace the form wording, got %q", err.Error())
	}
	if !errors.Is(err, errs.ErrDuplicateCode) {
		t.Error("BUG-074: sentinel match must hold with an override message")
	}
}

// TestBug074DoCreateUsesNeutralMessage 用例 4（端到端）：
// 真实走版本化 _doCreate 的重码分支，用户可见 msg 必须是中性文案。
func TestBug074DoCreateUsesNeutralMessage(t *testing.T) {
	db := openBug069DB(t, &bug074Doc{})
	svc := NewGenericService[*bug074Doc](repository.NewCRUDWithDB[*bug074Doc](db), Config[*bug074Doc]{
		VersionMode: true,
		VersionFields: &VersionFieldMapping{
			ULIDField: "ULID", CodeField: "Code", VersionField: "VersionCode",
			CurrentField: "IsCurrent", StatusField: "VersionStatus", ParentField: "ParentULID",
		},
	})
	ctx := context.Background()

	created, err := svc.Create(ctx, []CrudRequest[*bug074Doc]{
		&bug074Req[*bug074Doc]{data: map[string]any{"code": "tpl_dup", "name": "first"}},
	})
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if (*created[0]).Code != "tpl_dup" {
		t.Fatalf("code = %q, want tpl_dup", (*created[0]).Code)
	}

	// 同 code 再建 → 必须报重码
	_, err = svc.Create(ctx, []CrudRequest[*bug074Doc]{
		&bug074Req[*bug074Doc]{data: map[string]any{"code": "tpl_dup", "name": "second"}},
	})
	if err == nil {
		t.Fatal("duplicate code create must fail")
	}
	if !errors.Is(err, errs.ErrDuplicateCode) {
		t.Fatalf("err must match ErrDuplicateCode, got %v", err)
	}
	if strings.Contains(err.Error(), "form_code") || strings.Contains(err.Error(), "表单") {
		t.Errorf("BUG-074: user-visible message must not mention form, got %q", err.Error())
	}
}

// TestBug074DoCreateUsesOverrideMessage 用例 5（端到端）：
// 配置覆写后，真实重码错误使用实体专属文案。
func TestBug074DoCreateUsesOverrideMessage(t *testing.T) {
	db := openBug069DB(t, &bug074Doc{})
	svc := NewGenericService[*bug074Doc](repository.NewCRUDWithDB[*bug074Doc](db), Config[*bug074Doc]{
		VersionMode: true,
		VersionFields: &VersionFieldMapping{
			ULIDField: "ULID", CodeField: "Code", VersionField: "VersionCode",
			CurrentField: "IsCurrent", StatusField: "VersionStatus", ParentField: "ParentULID",
			DuplicateCodeMsg: "通知模板编码已存在，请更换 template_code",
		},
	})
	ctx := context.Background()

	if _, err := svc.Create(ctx, []CrudRequest[*bug074Doc]{
		&bug074Req[*bug074Doc]{data: map[string]any{"code": "tpl_o", "name": "a"}},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err := svc.Create(ctx, []CrudRequest[*bug074Doc]{
		&bug074Req[*bug074Doc]{data: map[string]any{"code": "tpl_o", "name": "b"}},
	})
	if err == nil {
		t.Fatal("duplicate code create must fail")
	}
	if !strings.Contains(err.Error(), "template_code") {
		t.Errorf("override message must reach the caller, got %q", err.Error())
	}
	if !errors.Is(err, errs.ErrDuplicateCode) {
		t.Error("sentinel match must hold with an override message")
	}
}
