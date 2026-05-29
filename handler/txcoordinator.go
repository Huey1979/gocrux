package handler

import (
	"context"

	"github.com/Huey1979/gocrux/common"

	"gorm.io/gorm"
)

// ============================================================
// TxCoordinator — 事务编排器
//
// 封装 gorm.DB，Handler 层不直接接触 gorm.DB。
// Handler 通过 TxCoordinator.Run 在事务内编排多个 Service 调用。
//
// 使用示例：
//
//	tc := NewTxCoordinator(db)
//	err := tc.Run(ctx, func(txCtx context.Context) error {
//	    parentResults, err := parentSvc.Create(txCtx, parentReqs)
//	    if err != nil { return err }
//	    // ... 级联创建子记录 ...
//	    return nil
//	})
//
// fn 回调中透传的 txCtx 已包含事务 tx，
// Service 层通过 service.GetTx(ctx) 获取事务 DB 实例。
// ============================================================

type TxCoordinator struct {
	db *gorm.DB
}

// NewTxCoordinator 创建事务编排器。
func NewTxCoordinator(db *gorm.DB) *TxCoordinator {
	return &TxCoordinator{db: db}
}

// Run 在事务内执行编排逻辑。
// fn 回调收到的 ctx 已注入事务 tx，所有 Service 调用共享同一事务。
// fn 返回 error 时自动回滚，返回 nil 时自动提交。
func (tc *TxCoordinator) Run(ctx context.Context, fn func(txCtx context.Context) error) error {
	return tc.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txCtx := common.WithTx(ctx, tx)
		return fn(txCtx)
	})
}
