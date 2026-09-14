// BUG-078 回归测试（后半，repository 侧）：比较条件的取值不归一，
// 无序 map（map[string]any / 已解码的 bson.M）会被驱动按无序编码，
// 与库中原本有序编码的文档比较得不到稳定结果。
//
// 修复：新增 bsonSafeValue —— 标量类型直接透传（省一次编码），
// 其余统一走 bson.Marshal/Unmarshal 归一化；失败时原样返回不吞信息。
// 所有比较条件（$eq/$ne/$gt/$gte/$lt/$lte/$in）均接入。
//
// 本文件为纯函数测试（不需要 Mongo 实例）。
package repository

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// TestBug078BsonSafeValueScalarsPassThrough 标量类型原样返回（不重复编码）。
func TestBug078BsonSafeValueScalarsPassThrough(t *testing.T) {
	now := time.Now()
	cases := []any{
		"str", true, 42, int64(7), uint(3), 1.5, float32(2.5), now,
		primitive.NewObjectID(), primitive.DateTime(12345),
	}
	for _, v := range cases {
		got := bsonSafeValue(v)
		switch want := v.(type) {
		case time.Time:
			gt, ok := got.(time.Time)
			if !ok || !gt.Equal(want) {
				t.Errorf("time.Time must pass through, got %T(%v)", got, got)
			}
		default:
			if got != v {
				t.Errorf("scalar %T(%v) must pass through unchanged, got %T(%v)", v, v, got, got)
			}
		}
	}
}

// TestBug078BsonSafeValueUnorderedMapNormalized 核心：无序 map 被归一化。
func TestBug078BsonSafeValueUnorderedMapNormalized(t *testing.T) {
	in := map[string]any{"b": 2, "a": 1}
	got := bsonSafeValue(in)

	// 归一化后应可被 bson 编码（能得到 bson.D 或等价的有序结构）
	if got == nil {
		t.Fatal("BUG-078: normalized value must not be nil")
	}
	if _, err := bson.Marshal(bson.M{"v": got}); err != nil {
		t.Fatalf("BUG-078: normalized value must be bson-encodable, got %T(%v): %v", got, got, err)
	}

	// 内容等价性：按键取值应与输入一致
	probe := map[string]int{}
	b, err := bson.Marshal(bson.M{"v": got})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out struct {
		V map[string]any `bson:"v"`
	}
	if err := bson.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for k, v := range out.V {
		if n, ok := v.(int32); ok {
			probe[k] = int(n)
		}
	}
	if probe["a"] != 1 || probe["b"] != 2 {
		t.Errorf("normalized map content mismatch: %#v", probe)
	}
}

// TestBug078BsonSafeValueNilSafe nil 原样返回（不 panic）。
func TestBug078BsonSafeValueNilSafe(t *testing.T) {
	if got := bsonSafeValue(nil); got != nil {
		t.Errorf("nil must pass through, got %T(%v)", got, got)
	}
}

// TestBug078FilterToBsonCompareOpsUseSafeValue 不回归 + 接线检查：
// 各比较运算仍生成正确的操作符，且标量值原样透传（行为不变）。
func TestBug078FilterToBsonCompareOpsUseSafeValue(t *testing.T) {
	cases := []struct {
		op   FilterOp
		key  string
		want any
	}{
		{OpEQ, "", "v"},
		{OpNEQ, "$ne", "v"},
		{OpGT, "$gt", "v"},
		{OpGTE, "$gte", "v"},
		{OpLT, "$lt", "v"},
		{OpLTE, "$lte", "v"},
	}
	for _, c := range cases {
		got := filterToBson(Filter{Field: "f", Op: c.op, Value: "v"})
		if c.key == "" {
			if got["f"] != c.want {
				t.Errorf("op %s: got %#v, want f=%v", c.op, got, c.want)
			}
			continue
		}
		cond, ok := got["f"].(bson.M)
		if !ok || cond[c.key] != c.want {
			t.Errorf("op %s: got %#v, want %s=%v", c.op, got, c.key, c.want)
		}
	}

	// OpIn 的切片值仍应是数组形态
	inGot := filterToBson(Filter{Field: "f", Op: OpIn, Value: []any{"a", "b"}})
	cond, ok := inGot["f"].(bson.M)
	if !ok {
		t.Fatalf("OpIn: got %#v", inGot)
	}
	items, ok := cond["$in"].(bson.A)
	if !ok || len(items) != 2 {
		t.Errorf("OpIn: $in = %T(%v), want bson.A with 2 items", cond["$in"], cond["$in"])
	}
}

// TestBug078TimeValueSurvivesFilter 时间值经比较运算后仍是时间类型
// （类型归一由 service 层完成，这里确认 repository 侧不破坏它）。
func TestBug078TimeValueSurvivesFilter(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	got := filterToBson(Filter{Field: "t", Op: OpGT, Value: now})
	cond, ok := got["t"].(bson.M)
	if !ok {
		t.Fatalf("got %#v", got)
	}
	back, ok := cond["$gt"].(time.Time)
	if !ok || !back.Equal(now) {
		t.Errorf("$gt = %T(%v), want time.Time %v", cond["$gt"], cond["$gt"], now)
	}
}
