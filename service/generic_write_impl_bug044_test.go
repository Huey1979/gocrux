package service

import (
	"reflect"
	"testing"
)

// bug044Doc 模拟含 type:json string 字段的 MySQL 实体。
type bug044Doc struct {
	ID      string   `gorm:"column:id;primaryKey"`
	Name    string   `gorm:"column:name"`
	Config  string   `gorm:"column:config;type:json"`
	Tags    string   `gorm:"column:tags;type:json"`
	Options []string `gorm:"column:options;type:json"`
	Count   int      `gorm:"column:count;type:json"`
	Flag    bool     `gorm:"column:flag;type:json"`
}

func TestNormalizeJSONValue(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(d *bug044Doc)
		field      string
		want       string
	}{
		{"空串 json 字段归一化为 null", func(d *bug044Doc) {}, "Config", "null"},
		{"已有合法 JSON 不被改写", func(d *bug044Doc) { d.Config = `{"a":1}` }, "Config", `{"a":1}`},
		{"显式传空串归一化为 null", func(d *bug044Doc) { d.Config = "" }, "Config", "null"},
		{"普通 string 字段空串不变", func(d *bug044Doc) {}, "Name", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := &bug044Doc{}
			c.mutate(d)
			normalizeJSONValue(d)
			v := reflect.ValueOf(d).Elem().FieldByName(c.field)
			if got := v.String(); got != c.want {
				t.Errorf("%s: got %q, want %q", c.field, got, c.want)
			}
		})
	}
}

func TestNormalizeJSONValueNonStringSkipped(t *testing.T) {
	// 非 string 的 type:json 字段（int/bool/slice）必须跳过，不被 SetString
	d := &bug044Doc{}
	normalizeJSONValue(d)
	if d.Count != 0 || d.Flag || len(d.Options) != 0 {
		t.Errorf("非 string 字段被意外修改: %#v", d)
	}
}

func TestNormalizeJSONValuePointerLevels(t *testing.T) {
	d := &bug044Doc{}
	var p **bug044Doc = &d
	normalizeJSONValue(p)
	if d.Config != "null" {
		t.Errorf("双层指针: Config = %q, want %q", d.Config, "null")
	}

	// nil 指针安全
	normalizeJSONValue((*bug044Doc)(nil))
	var nilPtr **bug044Doc
	normalizeJSONValue(nilPtr)
}
