package repository

import (
	"testing"
	"time"
)

// testSoftDelDoc 模拟 heims DocCentral 的多字段 SetDelete（Mongo 实体）。
type testSoftDelDoc struct {
	DocULID   string    `bson:"doc_ulid"`
	Title     string    `bson:"title"`
	IsDeleted int8      `bson:"is_deleted"`
	IsTrashed bool      `bson:"is_trashed"`
	TrashedAt time.Time `bson:"trashed_at"`
	DeletedAt time.Time `bson:"deleted_at"`
}

func (d *testSoftDelDoc) SetDelete() bool {
	d.IsDeleted = 1
	d.IsTrashed = true
	d.TrashedAt = time.Now()
	return true
}

func TestSoftDeleteExtraFields(t *testing.T) {
	extra := softDeleteExtraFields(softDeleteProbe[*testSoftDelDoc]())
	if len(extra) == 0 {
		t.Fatal("expected extra fields from SetDelete")
	}
	if v, ok := extra["is_trashed"]; !ok || v != true {
		t.Errorf("is_trashed missing or wrong: %#v", extra["is_trashed"])
	}
	if _, ok := extra["trashed_at"]; !ok {
		t.Error("trashed_at missing")
	}
	if _, ok := extra["title"]; ok {
		t.Error("title should be filtered (zero value)")
	}
	if _, ok := extra["deleted_at"]; ok {
		t.Error("deleted_at should be filtered (zero time)")
	}
	if _, ok := extra["doc_ulid"]; ok {
		t.Error("doc_ulid should be filtered (empty string)")
	}
}

func TestSoftDeleteSet(t *testing.T) {
	set := softDeleteSet(softDeleteProbe[*testSoftDelDoc]())
	if _, ok := set["is_deleted"]; !ok {
		t.Error("is_deleted missing in set")
	}
	if _, ok := set["is_trashed"]; !ok {
		t.Error("is_trashed missing in set")
	}
	if _, ok := set["title"]; ok {
		t.Error("title should not be in set (zero value)")
	}

	// 无 SetDelete 方法的实体：仅 is_deleted（与原行为一致）
	type plain struct {
		Name string `bson:"name"`
	}
	set2 := softDeleteSet(softDeleteProbe[plain]())
	if len(set2) != 1 {
		t.Errorf("expected only is_deleted, got %#v", set2)
	}
	if _, ok := set2["is_deleted"]; !ok {
		t.Error("is_deleted missing for plain entity")
	}
}
