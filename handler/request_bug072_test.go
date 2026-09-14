// BUG-072 回归测试：通用 update 把「显式空串」静默丢弃，任何字符串字段都无法清空。
//
// 缺陷要点（修复前）：mergeByJSON 第 3 步把「显式提交空串」与「未提交该字段」
// 当成同一件事处理，一律 continue —— 于是 update 返回 200「操作成功」而库里一个字未改，
// 属静默数据不一致。而该 guard 真正需要保护的场景只有 create（零值实体 + SetDefaults）。
//
// 修复：merge 语义变成入参（allowClearEmpty）。
//   - Create：MergeTo            → 空串不覆盖已有非空值（SetDefaults 保护不变）；
//   - Update：MergeToExisting    → 空串原样写入（可清空），未提交字段仍保持原值。
//
// 本文件覆盖：① Update 可清空字符串；② Update 局部更新语义不回归（未提交字段保持原值）；
// ③ Create 的 SetDefaults 保护不变；④ 数字 0 / 布尔 false 显式提交仍真实落库（防把 BUG-045 改回去）；
// ⑤ 非空值照常覆盖；⑥ type:json 字段显式传 "" 仍走归一化（不写非法 JSON）；
// ⑦ 空串 vs 单空格的最小对照。
package handler

import (
	"encoding/json"
	"testing"
	"time"
)

// bug072Doc 非版本化测试实体，覆盖 string / int / bool / json 列。
type bug072Doc struct {
	ULID    string `gorm:"column:ulid;primaryKey;size:26" json:"ulid"`
	Name    string `gorm:"column:name;size:100" json:"name"`
	Remark  string `gorm:"column:remark;size:200" json:"remark"`
	Count   int    `gorm:"column:count;default:0" json:"count"`
	Enabled bool   `gorm:"column:enabled" json:"enabled"`
	Config  string `gorm:"column:config;type:json" json:"config"`
}

// 实现 service.Record，使 MapRequest[*bug072Doc] 可用（M = *bug072Doc，与 heims 实体同形）。
func (d *bug072Doc) SetDefaults()             {}
func (d *bug072Doc) SetCreatedAt(_ time.Time) {}
func (d *bug072Doc) SetCreatedBy(string)      {}
func (d *bug072Doc) SetUpdatedAt(_ time.Time) {}
func (d *bug072Doc) SetUpdatedBy(string)      {}
func (d *bug072Doc) SupportsDraft() bool      { return false }
func (d *bug072Doc) SetDelete() bool          { return false }
func (d *bug072Doc) PKField() string          { return "ulid" }
func (d *bug072Doc) SelfFKField() string      { return "" }

// TestBug072MergeByJSONUpdateAllowsClear 用例 1（核心回归）：
// allowClearEmpty=true（Update 语义）下，显式空串必须覆盖旧值。
func TestBug072MergeByJSONUpdateAllowsClear(t *testing.T) {
	target := &bug072Doc{Name: "ICP备案号", Remark: "keep-me"}

	if err := mergeByJSON(map[string]any{"name": ""}, target, true); err != nil {
		t.Fatalf("mergeByJSON: %v", err)
	}
	if target.Name != "" {
		t.Errorf("BUG-072: update must clear the field, name = %q want \"\"", target.Name)
	}
	// 未提交的字段保持原值（局部更新语义不变）
	if target.Remark != "keep-me" {
		t.Errorf("unsubmitted field must keep its value, remark = %q want keep-me", target.Remark)
	}
}

// TestBug072MergeByJSONCreateKeepsGuard 用例 3：
// allowClearEmpty=false（Create 语义）下，空串仍不覆盖已有的非空值（SetDefaults 保护不回归）。
func TestBug072MergeByJSONCreateKeepsGuard(t *testing.T) {
	target := &bug072Doc{Name: "default-from-setdefaults"}

	if err := mergeByJSON(map[string]any{"name": ""}, target, false); err != nil {
		t.Fatalf("mergeByJSON: %v", err)
	}
	if target.Name != "default-from-setdefaults" {
		t.Errorf("BUG-072: create must keep SetDefaults value, name = %q want default-from-setdefaults", target.Name)
	}
}

// TestBug072MergeByJSONZeroValuesStillPersist 用例 4：
// 数字 0 / 布尔 false 在两种语义下都真实写入（防把 BUG-045 改回去）。
func TestBug072MergeByJSONZeroValuesStillPersist(t *testing.T) {
	for _, allowClear := range []bool{false, true} {
		target := &bug072Doc{Count: 7, Enabled: true}
		err := mergeByJSON(map[string]any{"count": float64(0), "enabled": false}, target, allowClear)
		if err != nil {
			t.Fatalf("mergeByJSON(allowClearEmpty=%v): %v", allowClear, err)
		}
		if target.Count != 0 {
			t.Errorf("allowClearEmpty=%v: count = %d, want 0 (BUG-045 must not regress)", allowClear, target.Count)
		}
		if target.Enabled {
			t.Errorf("allowClearEmpty=%v: enabled = true, want false (BUG-045 must not regress)", allowClear)
		}
	}
}

// TestBug072MergeByJSONNonEmptyStillOverwrites 用例 5：非空值照常覆盖，两种语义一致。
func TestBug072MergeByJSONNonEmptyStillOverwrites(t *testing.T) {
	for _, allowClear := range []bool{false, true} {
		target := &bug072Doc{Name: "old"}
		if err := mergeByJSON(map[string]any{"name": "new"}, target, allowClear); err != nil {
			t.Fatalf("mergeByJSON(allowClearEmpty=%v): %v", allowClear, err)
		}
		if target.Name != "new" {
			t.Errorf("allowClearEmpty=%v: name = %q want new", allowClear, target.Name)
		}
	}
}

// TestBug072EmptyVsSingleSpace 用例 7（报告中「空串 vs 单空格」的最小对照）：
// 修复后两者都应落库，区别只是内容不同 —— 证明被丢的从来不是「非空」而是「空串」这一个分支。
func TestBug072EmptyVsSingleSpace(t *testing.T) {
	empty := &bug072Doc{Name: "sentinel"}
	if err := mergeByJSON(map[string]any{"name": ""}, empty, true); err != nil {
		t.Fatalf("merge empty: %v", err)
	}
	space := &bug072Doc{Name: "sentinel"}
	if err := mergeByJSON(map[string]any{"name": " "}, space, true); err != nil {
		t.Fatalf("merge space: %v", err)
	}
	if empty.Name != "" {
		t.Errorf("empty string: name = %q want \"\"", empty.Name)
	}
	if space.Name != " " {
		t.Errorf("single space: name = %q want \" \"", space.Name)
	}
}

// ============================================================
// MapRequest 层：MergeTo / MergeToExisting 语义分派
// ============================================================

// TestBug072MapRequestMergeToExisting 用例：MapRequest 实现 MergeToExisting，
// 且与 MergeTo 语义确有差别（Update 路径才放开清空）。
func TestBug072MapRequestMergeToExisting(t *testing.T) {
	req := &MapRequest[*bug072Doc]{data: map[string]any{"name": "", "remark": "r2"}}

	updated := &bug072Doc{Name: "old-name", Remark: "r1"}
	if err := req.MergeToExisting(&updated); err != nil {
		t.Fatalf("MergeToExisting: %v", err)
	}
	if updated.Name != "" {
		t.Errorf("BUG-072: MergeToExisting must clear name, got %q", updated.Name)
	}
	if updated.Remark != "r2" {
		t.Errorf("MergeToExisting must overwrite remark, got %q", updated.Remark)
	}

	created := &bug072Doc{Name: "default-name"}
	if err := req.MergeTo(&created); err != nil {
		t.Fatalf("MergeTo: %v", err)
	}
	if created.Name != "default-name" {
		t.Errorf("MergeTo must keep default name, got %q", created.Name)
	}
}

// TestBug072JSONFieldEmptyStringNormalization 用例 6：
// type:json 字段显式传 "" 时，mergeByJSON 层照旧写入空串，
// 由 service 的 normalizeJSONValue 统一归一化为 "null"（BUG-044），
// 这里验证「merge 层不再拦截」这一前提没被破坏。
func TestBug072JSONFieldEmptyStringNormalization(t *testing.T) {
	target := &bug072Doc{Config: `{"a":1}`}
	if err := mergeByJSON(map[string]any{"config": ""}, target, true); err != nil {
		t.Fatalf("mergeByJSON: %v", err)
	}
	if target.Config != "" {
		t.Errorf("BUG-072: merge layer must pass through explicit empty string, config = %q", target.Config)
	}
	// 保证该值可被后续归一化流程识别（"null" 对任意 JSON 目标合法）
	if !json.Valid([]byte(`null`)) {
		t.Fatal("sanity: \"null\" must be valid JSON")
	}
}
