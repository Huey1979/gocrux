package expression

import (
	"testing"
)

func TestNormalize_Idempotent(t *testing.T) {
	input := `{"And":[{"Eq":["${status}","active"]},{"In":["admin","$operatorRoleIds"]}]}`
	out1, _ := Normalize(input)
	out2, _ := Normalize(out1)
	if out1 != out2 {
		t.Errorf("not idempotent:\n  first:  %s\n  second: %s", out1, out2)
	}
}

func TestNormalize_CommutativeAnd(t *testing.T) {
	// ("status","active") < ("$operatorRoleIds","admin") 按 JSON 排序
	a := `{"And":[{"Eq":["${status}","active"]},{"In":["$operatorRoleIds","admin"]}]}`
	b := `{"And":[{"In":["$operatorRoleIds","admin"]},{"Eq":["${status}","active"]}]}`
	na, _ := Normalize(a)
	nb, _ := Normalize(b)
	if na != nb {
		t.Errorf("commutative And failed:\n  a: %s\n  b: %s", na, nb)
	}
}

func TestNormalize_EqVariableLeft(t *testing.T) {
	// 变量在左，字面量在右 —— 两种写法应归一
	a := `{"Eq":["${status}","active"]}`
	b := `{"Eq":["active","${status}"]}`
	na, _ := Normalize(a)
	nb, _ := Normalize(b)
	if na != nb {
		t.Errorf("Eq ordering failed:\n  a: %s\n  b: %s", na, nb)
	}
}

func TestNormalize_Nested(t *testing.T) {
	input := `{"And":[{"Gt":["${amount}","1000"]},{"Or":[{"In":["admin","$operatorRoleIds"]},{"Eq":["$operatorId","${adderId}"]}]}]}`
	out, err := Normalize(input)
	if err != nil {
		t.Fatal(err)
	}
	// Run again to ensure idempotent
	out2, _ := Normalize(out)
	if out != out2 {
		t.Errorf("nested not idempotent:\n  first:  %s\n  second: %s", out, out2)
	}
}

func TestNormalize_MongoQuery(t *testing.T) {
	// MongoDB 查询表达式：普通 JSON，只排序 key
	input := `{"status":"active","amount":{"$gt":"1000"},"dept":{"cal":"${dept_ulid}"}}`
	out, _ := Normalize(input)
	// key 应该是 amount, dept, status （字母序）
	if out != `{"amount":{"$gt":"1000"},"dept":{"cal":"${dept_ulid}"},"status":"active"}` {
		t.Errorf("mongo query failed: %s", out)
	}
}
