package common

import (
	"context"

	"gorm.io/gorm"
)

// ============================================================
// ctx key — 事务 tx
// TxCoordinator 将事务 DB 注入 ctx，Repository 通过 GetTx 取用。
// ============================================================

// CtxKeyTx 事务 DB 的 context key 常量
const CtxKeyTx = "tx"

// WithTx 将事务 DB 注入 ctx。
func WithTx(ctx context.Context, tx *gorm.DB) context.Context {
	return context.WithValue(ctx, CtxKeyTx, tx)
}

// GetTx 从 ctx 取出事务 DB（可能为 nil）。
func GetTx(ctx context.Context) *gorm.DB {
	if v := ctx.Value(CtxKeyTx); v != nil {
		if tx, ok := v.(*gorm.DB); ok {
			return tx
		}
	}
	return nil
}
