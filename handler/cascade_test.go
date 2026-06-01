package handler

import (
	"context"
	"testing"
)

// ============================================================
// parseStopRule / parseStopRules 单元测试
// ============================================================

func TestParseStopRule(t *testing.T) {
	tests := []struct {
		input   string
		want    StopRule
		wantErr bool
	}{
		{
			input:   "-department:manager",
			want:    StopRule{OnHandler: "department", Field: "manager", Stop: true},
			wantErr: false,
		},
		{
			input:   "department:parent_id",
			want:    StopRule{OnHandler: "department", Field: "parent_id", Stop: false},
			wantErr: false,
		},
		{
			input:   "dept:site_ulid",
			want:    StopRule{OnHandler: "dept", Field: "site_ulid", Stop: false},
			wantErr: false,
		},
		{
			input:   "-site:parent_site_ulid",
			want:    StopRule{OnHandler: "site", Field: "parent_site_ulid", Stop: true},
			wantErr: false,
		},
		{
			input:   "",
			wantErr: true,
		},
		{
			input:   "abc",
			wantErr: true,
		},
		{
			input:   ":field",
			wantErr: true,
		},
		{
			input:   "handler:",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseStopRule(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("期望错误但未返回")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseStopRule 失败: %v", err)
			}
			if got.OnHandler != tt.want.OnHandler || got.Field != tt.want.Field || got.Stop != tt.want.Stop {
				t.Fatalf("parseStopRule(%q) = %+v, want %+v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseStopRules(t *testing.T) {
	rules, err := parseStopRules("-department:manager,department:parent_id,dept:site_ulid")
	if err != nil {
		t.Fatalf("parseStopRules 失败: %v", err)
	}
	if len(rules) != 3 {
		t.Fatalf("期望 3 条规则, 实际 %d", len(rules))
	}
	if !rules[0].Stop || rules[0].OnHandler != "department" || rules[0].Field != "manager" {
		t.Fatalf("rules[0] = %+v", rules[0])
	}
	if rules[1].Stop || rules[1].OnHandler != "department" || rules[1].Field != "parent_id" {
		t.Fatalf("rules[1] = %+v", rules[1])
	}
	if rules[2].Stop || rules[2].OnHandler != "dept" || rules[2].Field != "site_ulid" {
		t.Fatalf("rules[2] = %+v", rules[2])
	}

	rules2, err := parseStopRules("")
	if err != nil {
		t.Fatalf("parseStopRules(\"\") 失败: %v", err)
	}
	if len(rules2) != 0 {
		t.Fatalf("空字符串应返回空列表, 实际 %d", len(rules2))
	}
}

// ============================================================
// fieldLimitMap / effectiveExpandDepth 单元测试
// ============================================================

func TestFieldLimitMap(t *testing.T) {
	ctx := context.Background()

	if fl := getFieldLimits(ctx); fl != nil {
		t.Fatal("未设置 fieldLimits 应返回 nil")
	}

	fl := fieldLimitMap{"manager": 0, "parent_id": 1}
	ctx2 := withFieldLimits(ctx, fl)

	got := getFieldLimits(ctx2)
	if got == nil {
		t.Fatal("设置后应能获取到")
	}
	if got["manager"] != 0 {
		t.Fatalf("manager 应为 0, 实际 %d", got["manager"])
	}
	if got["parent_id"] != 1 {
		t.Fatalf("parent_id 应为 1, 实际 %d", got["parent_id"])
	}

	if fl2 := getFieldLimits(ctx); fl2 != nil {
		t.Fatal("原 ctx 不应被修改")
	}
}

func TestEffectiveExpandDepth(t *testing.T) {
	depth, ok := effectiveExpandDepth(context.Background(), false, "manager")
	if !ok || depth != 0 {
		t.Fatalf("无 fieldLimits 无 depth: want (0, true), got (%d, %v)", depth, ok)
	}

	ctx := withDepth(context.Background(), 0)
	_, ok2 := effectiveExpandDepth(ctx, true, "manager")
	if ok2 {
		t.Fatalf("depth=0: want (_, false), got (_, true)")
	}

	ctx3 := withFieldLimits(context.Background(), fieldLimitMap{"manager": 0})
	_, ok3 := effectiveExpandDepth(ctx3, true, "manager")
	if ok3 {
		t.Fatalf("fieldLimit manager=0: want (_, false), got (_, true)")
	}

	ctx4 := withFieldLimits(context.Background(), fieldLimitMap{"parent_id": 1})
	depth4, ok4 := effectiveExpandDepth(ctx4, true, "parent_id")
	if !ok4 || depth4 != 1 {
		t.Fatalf("fieldLimit parent_id=1: want (1, true), got (%d, %v)", depth4, ok4)
	}

	ctx5 := withFieldLimits(withDepth(context.Background(), 3), fieldLimitMap{"other": 1})
	depth5, ok5 := effectiveExpandDepth(ctx5, true, "unrelated")
	if !ok5 || depth5 != 3 {
		t.Fatalf("fieldLimit 不匹配: want (3, true), got (%d, %v)", depth5, ok5)
	}
}

// TestParseStopRulesCompat 兼容性：测试空余和空白场景
func TestSplitCSV(t *testing.T) {
	if len(splitCSV("")) != 0 {
		t.Fatal("空字符串应返回空列表")
	}
	if len(splitCSV("a,b,c")) != 3 {
		t.Fatal("a,b,c 应返回 3 个元素")
	}
	parts := splitCSV(" a ,  b , c ")
	if len(parts) != 3 || parts[0] != "a" || parts[1] != "b" || parts[2] != "c" {
		t.Fatalf("trimmed split: got %v", parts)
	}
}
