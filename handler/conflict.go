package handler

import (
	"context"
	"errors"
	"strings"

	errs "github.com/Huey1979/gocrux/errors"
	"github.com/sirupsen/logrus"

	"go.mongodb.org/mongo-driver/mongo"
	"gorm.io/gorm"
)

// ============================================================
// 主键冲突处理（设计文档 §七）
//
// 冲突概率：ULID = 10 字符毫秒时间戳 + 16 字符 crypto/rand 随机数，
// 碰撞需「同一毫秒 + 16 字符随机相同」≈ 32^-16 ≈ 1.2e-24。
// **因此调用方不需要为防冲突查库** —— 只需在真发生时能正确收场。
//
// 两种部署形态（与 A2 一致：框架必须同时支持）：
//
//	有事务（MySQL / Mongo 副本集）
//	  冲突 → 事务整体回滚 → 重试 1 次 → 仍失败则报错
//	  重试由 TxCoordinator 负责，需**显式声明**（SetRetryOnPKConflict）
//
//	无事务（Mongo 单机）
//	  冲突 → 不重试，报错 + 尽力而为标记本次写入的全部记录 is_deleted=1 + ERROR 日志
//	  关键：主流程**不依赖清理成功**（否则会出现"以为干净了其实没干净"的错觉）
//
// 重试语义（§7.4）：
//
//	❌ 前端"重放当前请求体"（会复用旧 ticket 与旧 ULID）
//	✅ 前端**重新发起整个请求**（新 ticket、新预分配）
//
// 错误文案会明确写出这一点。
// ============================================================

// isPKConflictError 判断错误是否为主键冲突（重复键）。
//
// 识别三种数据源：
//
//	MySQL ：Error 1062 (ER_DUP_ENTRY) —— 同时要求错误文本含 "Duplicate entry"
//	        （只匹配 1062 数字会误伤「SQL 里恰好出现 1062」的查询失败）
//	SQLite：UNIQUE constraint failed: <table>.<column>（1555 = 主键冲突）
//	        —— 嵌入式部署与测试用；glebarez 驱动不做 ErrDuplicatedKey 翻译，
//	        若不识别，冲突会以裸错误上抛，绕过整树重试与标删兜底。
//	Mongo ：E11000 duplicate key error（WriteException / CommandError 的 code 11000）
//
// 注意：这里**不**复用 errs.ErrDuplicateCode —— 那是业务编码重复（版本化实体
// Create 时的 code 唯一性），属于「用户应改 code 后重试」的业务错误；
// 而主键冲突是随机事件，处理策略完全不同（整树回滚重试 / 标删兜底）。
func isPKConflictError(err error) bool {
	if err == nil {
		return false
	}
	// MySQL
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	if msg := err.Error(); msg != "" {
		if strings.Contains(msg, "Error 1062") && strings.Contains(msg, "Duplicate entry") {
			return true
		}
		// SQLite：唯一索引/主键冲突。用完整短语匹配（而非只看 SQLITE_CONSTRAINT）
		// 以免把外键约束等其它约束错误误判为主键冲突。
		if strings.Contains(msg, "UNIQUE constraint failed") {
			return true
		}
	}
	// MongoDB
	var we mongo.WriteException
	if errors.As(err, &we) {
		for _, e := range we.WriteErrors {
			if e.Code == 11000 {
				return true
			}
		}
		return we.HasErrorCode(11000)
	}
	var ce mongo.CommandError
	if errors.As(err, &ce) && ce.Code == 11000 {
		return true
	}
	return false
}

// wrapPKConflict 把底层冲突错误统一包装为框架哨兵（保留原始错误链）。
func wrapPKConflict(err error) error {
	if err == nil {
		return nil
	}
	return errs.PKConflictError(err)
}

// cleanupAfterPKConflict 无事务部署下的冲突兜底：报错 + 尽力标删 + ERROR 日志。
//
// 三个要点（§7.3）：
//
//  1. **清理是"尽力而为"，不是"保证"** —— 返回值不依赖它成功；
//  2. **清理范围是本次写入的全部记录**（不只顶级 —— 孤儿子记录同样会暴露），
//     清单来自预分配注册表的已落库登记；
//  3. **必须 ERROR 日志**：10^-24 的事真发生了，很可能不是巧合
//     （生成器异常 / 主键空间污染），必须留痕。
//
// 返回的错误是给调用方（HTTP 层）的最终错误。
func (h *GenericHandler[M]) cleanupAfterPKConflict(ctx context.Context, cause error) error {
	reg := preallocRegistryFrom(ctx)
	written := reg.Written()

	// 3. ERROR 日志（含 ticket、冲突主键、实体清单）
	entityList := make([]string, 0, len(written))
	for _, w := range written {
		entityList = append(entityList, w.entityType+":"+w.ulid)
	}
	logrus.Errorf("gocrux: 预分配主键冲突（entity=%s ticket=%s 本次已写入 %d 条: %s）：%v；"+
		"无事务部署不重试，请重新发起整个请求（不要重放同一请求体，那会复用旧 ULID）",
		h.svcName, ticketFrom(ctx), len(written), strings.Join(entityList, ", "), cause)

	// 2. 尽力标删本次写入的全部记录
	cleanupErrs := h.markWrittenDeleted(ctx, written)
	if len(cleanupErrs) > 0 {
		// 清理失败不影响返回给调用方的错误（主流程不依赖清理成功）
		logrus.Errorf("gocrux: 冲突兜底清理未全部成功（entity=%s 失败 %d/%d 条）: %v",
			h.svcName, len(cleanupErrs), len(written), cleanupErrs)
	}

	return errs.PKConflictError(cause)
}

// markWrittenDeleted 尽力把本次已落库记录标记为已删（无事务冲突兜底）。
//
// 按实体类型分组：同一 Handler 的记录一次批量标删，减少往返。
// 返回失败项（供上层记日志），**不**因失败中断 —— 清理是尽力而为。
func (h *GenericHandler[M]) markWrittenDeleted(ctx context.Context, written []writtenRecord) []error {
	if len(written) == 0 || h.handlerReg == nil {
		return nil
	}
	// 按实体类型归拢
	byEntity := make(map[string][]any)
	for _, w := range written {
		if w.ulid == "" {
			continue
		}
		byEntity[w.entityType] = append(byEntity[w.entityType], w.ulid)
	}

	var failures []error
	for entityType, ids := range byEntity {
		child := h.handlerReg.Get(entityType)
		if child == nil {
			// 目标 Handler 未注册 → 无法清理，记录下来但不视为致命
			failures = append(failures, errs.ErrRecordNotFound)
			logrus.Warnf("gocrux: 冲突兜底无法清理未注册的实体 %q（%d 条）", entityType, len(ids))
			continue
		}
		// 走 DoDelete：软删实体写 is_deleted=1，物理删实体则真删。
		// 用独立的 ctx（原 ctx 可能已随事务回滚而失效）。
		cleanupCtx := context.WithoutCancel(ctx)
		if err := child.DoDelete(cleanupCtx, ids); err != nil {
			failures = append(failures, err)
			logrus.Warnf("gocrux: 冲突兜底清理失败（entity=%s ids=%v）: %v", entityType, ids, err)
		}
	}
	return failures
}
