// BUG-073 配套回归：service 层过滤值按目标列类型归一化。
//
// 背景：List 过滤值全部来自 URL 查询参数（类型恒为 string），而 Mongo 的比较
// 遵循 BSON type bracketing —— `ISODate(...)` 与 `"2020-01-01T00:00:00Z"`
// 比不出大小关系，于是时间/数值列的 :gt/:lt/:between 恒空集且不报错
// （MySQL 侧靠 DB 隐式转换恰好可用，同一套语义一真一假）。
//
// 修复：在构造 repository.Filter 之前用 normalizeFilterValue 按**目标列的
// Go 字段类型**归一化，MySQL 与 Mongo 拿到同一份已归一的 Value。
//
// 本文件为纯函数测试（不需要数据库）。
package service

import (
	"reflect"
	"testing"
	"time"

	"github.com/Huey1979/gocrux/repository"
)

// bug073Doc 覆盖 time / int / float / bool / string 列的测试实体。
type bug073Doc struct {
	ULID       string     `gorm:"column:ulid;primaryKey;size:26" json:"ulid" bson:"ulid"`
	StartTime  time.Time  `gorm:"column:start_time" json:"start_time" bson:"start_time"`
	EndTime    *time.Time `gorm:"column:end_time" json:"end_time" bson:"end_time"`
	DurationMS int        `gorm:"column:duration_ms" json:"duration_ms" bson:"duration_ms"`
	RetryCount int64      `gorm:"column:retry_count" json:"retry_count" bson:"retry_count"`
	Ratio      float64    `gorm:"column:ratio" json:"ratio" bson:"ratio"`
	Enabled    bool       `gorm:"column:enabled" json:"enabled" bson:"enabled"`
	TaskCode   string     `gorm:"column:task_code" json:"task_code" bson:"task_code"`
}

func (d *bug073Doc) SetDefaults()             {}
func (d *bug073Doc) SetCreatedAt(_ time.Time) {}
func (d *bug073Doc) SetCreatedBy(string)      {}
func (d *bug073Doc) SetUpdatedAt(_ time.Time) {}
func (d *bug073Doc) SetUpdatedBy(string)      {}
func (d *bug073Doc) SupportsDraft() bool      { return false }
func (d *bug073Doc) SetDelete() bool          { return false }
func (d *bug073Doc) PKField() string          { return "ulid" }
func (d *bug073Doc) SelfFKField() string      { return "" }

// TestBug073TimeColumnNormalized 核心回归：
// 时间列的字符串过滤值必须被解析成 time.Time（否则 Mongo 侧恒空集）。
func TestBug073TimeColumnNormalized(t *testing.T) {
	got := normalizeFilterValue[*bug073Doc]("start_time", repository.OpGT, "2020-01-01T00:00:00Z")
	tm, ok := got.(time.Time)
	if !ok {
		t.Fatalf("BUG-073: time column value must become time.Time, got %T (%v)", got, got)
	}
	want, _ := time.Parse(time.RFC3339, "2020-01-01T00:00:00Z")
	if !tm.Equal(want) {
		t.Errorf("parsed time = %v, want %v", tm, want)
	}
}

// TestBug073TimeColumnVariousLayouts 报告 §2.2 实测过的几种格式都要能解析。
func TestBug073TimeColumnVariousLayouts(t *testing.T) {
	inputs := []string{
		"2020-01-01T00:00:00Z",
		"2026-01-01T00:00:00+08:00",
		"2026-01-01 00:00:00",
		"2026-01-01",
	}
	for _, in := range inputs {
		got := normalizeFilterValue[*bug073Doc]("start_time", repository.OpGT, in)
		if _, ok := got.(time.Time); !ok {
			t.Errorf("BUG-073: %q must parse into time.Time, got %T", in, got)
		}
	}
}

// TestBug073TimePointerColumn 指针时间列同样归一（*time.Time → time.Time）。
func TestBug073TimePointerColumn(t *testing.T) {
	got := normalizeFilterValue[*bug073Doc]("end_time", repository.OpLT, "2026-12-31T23:59:59Z")
	if _, ok := got.(time.Time); !ok {
		t.Errorf("BUG-073: *time.Time column must normalize to time.Time, got %T", got)
	}
}

// TestBug073NumericColumnNormalized 数值列（报告 G10/N01 用例）。
func TestBug073NumericColumnNormalized(t *testing.T) {
	if got := normalizeFilterValue[*bug073Doc]("duration_ms", repository.OpGT, "0"); got != int64(0) {
		t.Errorf("int column: got %T(%v), want int64(0)", got, got)
	}
	if got := normalizeFilterValue[*bug073Doc]("retry_count", repository.OpLT, "zzzz"); got != "zzzz" {
		// 解析失败保持原样 —— 与 MySQL 侧「匹配不到」行为一致，不抛错
		t.Errorf("unparsable numeric must pass through, got %T(%v)", got, got)
	}
	if got := normalizeFilterValue[*bug073Doc]("ratio", repository.OpGTE, "1.5"); got != 1.5 {
		t.Errorf("float column: got %T(%v), want 1.5", got, got)
	}
}

// TestBug073BoolColumnNormalized 布尔列。
func TestBug073BoolColumnNormalized(t *testing.T) {
	for _, in := range []any{"true", "1", "yes"} {
		if got := normalizeFilterValue[*bug073Doc]("enabled", repository.OpEQ, in); got != true {
			t.Errorf("bool %v: got %T(%v), want true", in, got, got)
		}
	}
	if got := normalizeFilterValue[*bug073Doc]("enabled", repository.OpEQ, "0"); got != false {
		t.Errorf("bool \"0\": got %T(%v), want false", got, got)
	}
}

// TestBug073StringColumnUntouched 字符串列不受影响（回归防线）。
func TestBug073StringColumnUntouched(t *testing.T) {
	got := normalizeFilterValue[*bug073Doc]("task_code", repository.OpEQ, "qa_p5_at_a_xxx")
	if got != "qa_p5_at_a_xxx" {
		t.Errorf("string column must pass through, got %T(%v)", got, got)
	}
}

// TestBug073RangeBoundsNormalized OpRange（between）逐元素归一化。
func TestBug073RangeBoundsNormalized(t *testing.T) {
	got := normalizeFilterValue[*bug073Doc]("start_time", repository.OpRange,
		[]any{"2026-01-01T00:00:00+08:00", "2030-01-01T00:00:00+08:00"})

	items, ok := got.([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("BUG-073: range must stay a 2-element slice, got %T(%v)", got, got)
	}
	if _, ok := items[0].(time.Time); !ok {
		t.Errorf("lower bound = %T, want time.Time", items[0])
	}
	if _, ok := items[1].(time.Time); !ok {
		t.Errorf("upper bound = %T, want time.Time", items[1])
	}
}

// TestBug073RangeOpenEndedBounds 单侧区间：空串侧归一为 nil。
func TestBug073RangeOpenEndedBounds(t *testing.T) {
	got := normalizeFilterValue[*bug073Doc]("start_time", repository.OpRange,
		[]any{"2026-01-01T00:00:00Z", ""})
	items, ok := got.([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("got %T(%v)", got, got)
	}
	if _, ok := items[0].(time.Time); !ok {
		t.Errorf("lower bound = %T, want time.Time", items[0])
	}
	if items[1] != nil {
		t.Errorf("empty upper bound must become nil, got %T(%v)", items[1], items[1])
	}
}

// TestBug073InValuesNormalized :in 逐元素归一化。
func TestBug073InValuesNormalized(t *testing.T) {
	got := normalizeFilterValue[*bug073Doc]("duration_ms", repository.OpIn,
		[]any{"1", "2", "3"})
	items, ok := got.([]any)
	if !ok || len(items) != 3 {
		t.Fatalf("got %T(%v)", got, got)
	}
	for i, want := range []int64{1, 2, 3} {
		if items[i] != want {
			t.Errorf("items[%d] = %T(%v), want int64(%d)", i, items[i], items[i], want)
		}
	}
}

// TestBug073UnknownColumnPassThrough 未知列（不在实体里）原样透传，不做猜测。
func TestBug073UnknownColumnPassThrough(t *testing.T) {
	got := normalizeFilterValue[*bug073Doc]("no_such_column", repository.OpGT, "2026-01-01T00:00:00Z")
	if got != "2026-01-01T00:00:00Z" {
		t.Errorf("unknown column must pass through unchanged, got %T(%v)", got, got)
	}
}

// TestBug073AlreadyTypedValuesUnchanged 已是目标类型的值原样返回（幂等）。
func TestBug073AlreadyTypedValuesUnchanged(t *testing.T) {
	tm := time.Now()
	got := normalizeFilterValue[*bug073Doc]("start_time", repository.OpGT, tm)
	back, ok := got.(time.Time)
	if !ok || !back.Equal(tm) {
		t.Errorf("already-typed time.Time must be preserved, got %T(%v)", got, got)
	}

	if got := normalizeFilterValue[*bug073Doc]("duration_ms", repository.OpGT, 7); got != int64(7) {
		t.Errorf("already-typed int must be preserved, got %T(%v)", got, got)
	}
}

// sanity：确保 reflect 断言用到的类型与 goKindToRuleType 无关（纯类型文档）
func TestBug073ColumnKindOf(t *testing.T) {
	cases := []struct {
		col  string
		want reflect.Kind
	}{
		{"start_time", reflect.Struct},
		{"end_time", reflect.Struct}, // *time.Time 解指针后仍是 Struct
		{"duration_ms", reflect.Int},
		{"retry_count", reflect.Int64},
		{"ratio", reflect.Float64},
		{"enabled", reflect.Bool},
		{"task_code", reflect.String},
		{"no_such_column", reflect.Invalid},
	}
	for _, c := range cases {
		if got := columnKindOf[*bug073Doc](c.col); got != c.want {
			t.Errorf("columnKindOf(%s) = %v, want %v", c.col, got, c.want)
		}
	}
}
