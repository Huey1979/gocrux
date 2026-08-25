package repository

import (
	"errors"
	"testing"

	errs "github.com/Huey1979/gocrux/errors"
	"go.mongodb.org/mongo-driver/mongo"
)

// TestMapMongoFindErrorNoDocumentsIsSentinel BUG-062 核心：
// mongo.ErrNoDocuments 必须归一为 errs.ErrRecordNotFound 哨兵错误，
// 供 handler/errors.go mapServiceError 经 errors.Is 识别为 404（CodeNotFound）。
// 修复前返回裸 fmt.Errorf("record not found")，errors.Is 匹配不到任何哨兵，
// 落入默认分支映射 500。
func TestMapMongoFindErrorNoDocumentsIsSentinel(t *testing.T) {
	err := mapMongoFindError(mongo.ErrNoDocuments)
	if err == nil {
		t.Fatal("mapMongoFindError(ErrNoDocuments) must not be nil")
	}
	if !errors.Is(err, errs.ErrRecordNotFound) {
		t.Errorf("mapMongoFindError(ErrNoDocuments) = %v, must satisfy errors.Is(err, errs.ErrRecordNotFound)", err)
	}
	// 回归对照：裸 fmt.Errorf("record not found") 不满足哨兵（模拟修复前行为，反向验证语义）
	legacy := errors.New("record not found")
	if errors.Is(legacy, errs.ErrRecordNotFound) {
		t.Error("sanity: bare legacy error must NOT satisfy sentinel (this is the bug)")
	}
}

// TestMapMongoFindErrorWrapsOtherError BUG-062 防御：
// 非 ErrNoDocuments 的 Mongo 错误保持包装语义（保留原错误链，不误归一）。
func TestMapMongoFindErrorWrapsOtherError(t *testing.T) {
	original := errors.New("connection refused")
	err := mapMongoFindError(original)
	if !errors.Is(err, original) {
		t.Errorf("mapMongoFindError(other) = %v, must wrap original error", err)
	}
	if errors.Is(err, errs.ErrRecordNotFound) {
		t.Error("non-NoDocuments error must NOT map to ErrRecordNotFound")
	}
}
