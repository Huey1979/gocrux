package service

import (
	"context"

	"github.com/Huey1979/gocrux/common"

	"gorm.io/gorm"
)

// ============================================================
// ctx key — 事务 tx（透传至 internal/common/tx.go）
// ============================================================

// WithTx 将事务 DB 注入 ctx。
func WithTx(ctx context.Context, tx *gorm.DB) context.Context {
	return common.WithTx(ctx, tx)
}

// GetTx 从 ctx 取出事务 DB（可能为 nil）。
func GetTx(ctx context.Context) *gorm.DB {
	return common.GetTx(ctx)
}
