package common

import (
	"context"

	"go.mongodb.org/mongo-driver/mongo"
	"gorm.io/gorm"
)

// ============================================================
// ctx key — 事务
//
// MySQL:  TxCoordinator 将 *gorm.DB 注入 ctx，CRUDRepository 通过 GetTx 取用。
// Mongo:  TxCoordinator 将 mongo.Session 注入 ctx，MongoCRUDRepository 通过 GetMongoSession 取用。
// ============================================================

// CtxKeyTx 事务 DB 的 context key
const CtxKeyTx = "tx"

// CtxKeyMongoSession MongoDB 事务 session 的 context key
const CtxKeyMongoSession = "mongo_session"

// WithTx 将 GORM 事务 DB 注入 ctx。
func WithTx(ctx context.Context, tx *gorm.DB) context.Context {
	return context.WithValue(ctx, CtxKeyTx, tx)
}

// GetTx 从 ctx 取出 GORM 事务 DB（可能为 nil）。
func GetTx(ctx context.Context) *gorm.DB {
	if v := ctx.Value(CtxKeyTx); v != nil {
		if tx, ok := v.(*gorm.DB); ok {
			return tx
		}
	}
	return nil
}

// WithMongoSession 将 MongoDB session 注入 ctx。
func WithMongoSession(ctx context.Context, sess mongo.Session) context.Context {
	return context.WithValue(ctx, CtxKeyMongoSession, sess)
}

// GetMongoSession 从 ctx 取出 MongoDB session（可能为 nil）。
func GetMongoSession(ctx context.Context) mongo.Session {
	if v := ctx.Value(CtxKeyMongoSession); v != nil {
		if sess, ok := v.(mongo.Session); ok {
			return sess
		}
	}
	return nil
}
