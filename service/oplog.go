package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Huey1979/gocrux/common"
	"github.com/Huey1979/gocrux/internal/model/entity"
	"github.com/Huey1979/gocrux/repository"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// ============================================================
// 操作日志公开契约（BUG-077）
//
// 背景：`Config.EnableOpLog` 原本只有一个注入口 `SetOpLogRepo`，而它的参数类型
// `*repository.CRUDRepository[entity.SysOperationLog]` 里的 `entity` 包位于
// `internal/`，**模块外无法命名该类型** —— 下游应用无论如何配置都注入不了，
// `EnableOpLog` 永久空转且不报错。
//
// 本文件把操作日志的对外契约收敛到公开类型上：
//   - `OpLogRecord`：写入方与自定义实现方共用的记录结构（含可选前后快照）；
//   - `OpLogWriter`：落库方式的抽象，应用可自行实现（Mongo / 文件 / 消息队列）；
//   - `SetOpLogDB(*gorm.DB)`：不想自定义时的最省事注入口（gorm.DB 是公开可命名类型）；
//   - `SetOpLogWriter(OpLogWriter)`：完全自定义入口。
//
// 三者可任选其一。都不注入而 `EnableOpLog=true` 时，构造期会打印一次告警，
// 不再静默空转。
// ============================================================

// OpLogRecord 一条操作日志记录（框架 → 写入方的数据传输对象）。
//
// 字段语义与内置 sys_operation_log 表一致：只承载「谁、何时、对哪条记录、做了什么」
// 这类元数据；`RecordBefore` / `RecordAfter` 为可选的前后快照，
// 框架只在能低成本拿到快照的位置填充（update / delete），其余位置留 nil。
type OpLogRecord struct {
	EntityType   string    // 实体名（Config.EntityName）
	EntityID     string    // 被操作记录的主键；批量/按字段删除时为逗号分隔列表
	Operation    string    // create / update / delete / activate / updateVersion / restore
	OperatorULID string    // 操作人 ULID（GetUserULID(ctx)）
	RequestID    string    // 请求追踪 ID（GetRequestID(ctx)）
	OperatedAt   time.Time // 操作发生时间

	// 可选前后快照（JSON 原文，未注入则为 nil）。
	// 内置 MySQL 实现不使用这两个字段（表无对应列），
	// 应用自定义写入方（如落 MongoDB）可自行利用。
	RecordBefore json.RawMessage
	RecordAfter  json.RawMessage
}

// OpLogWriter 操作日志写入方抽象。
// 实现方需自行保证并发安全与幂等语义；框架按「尽力而为」调用，失败只记日志。
type OpLogWriter interface {
	// WriteOpLogs 批量写入操作日志。len(records)==0 时应直接返回 nil。
	WriteOpLogs(ctx context.Context, records []OpLogRecord) error
}

// ============================================================
// 内置 MySQL 实现（sys_operation_log）
// ============================================================

// dbOpLogWriter 基于 *gorm.DB 的内置实现，落 sys_operation_log 表。
type dbOpLogWriter struct {
	db *gorm.DB
}

// WriteOpLogs 实现 OpLogWriter：逐条构造内置实体后批量插入。
func (w *dbOpLogWriter) WriteOpLogs(ctx context.Context, records []OpLogRecord) error {
	if w == nil || w.db == nil || len(records) == 0 {
		return nil
	}
	logs := make([]*entity.SysOperationLog, 0, len(records))
	for _, r := range records {
		operatedAt := r.OperatedAt
		if operatedAt.IsZero() {
			operatedAt = time.Now()
		}
		logs = append(logs, &entity.SysOperationLog{
			LogULID:      common.NewULID(),
			EntityType:   r.EntityType,
			EntityID:     r.EntityID,
			Operation:    r.Operation,
			OperatorULID: r.OperatorULID,
			RequestID:    r.RequestID,
			OperatedAt:   operatedAt,
		})
	}
	// 与旧实现一致：使用 CRUDRepository 批量插入，保持列映射与迁移口径统一。
	repo := repository.NewCRUDWithDB[entity.SysOperationLog](w.db)
	return repo.InsertBatch(ctx, logs)
}

// SetOpLogDB 注入操作日志默认库（最省事的启用方式，BUG-077）。
//
// 传 nil 等价于未注入。启用示例（模块外可直接编译）：
//
//	svc := service.NewGenericService(repo, service.Config[entity.SysRole]{
//	    EntityName:  "role",
//	    EnableOpLog: true,
//	})
//	svc.SetOpLogDB(db) // db *gorm.DB
func (s *GenericService[M]) SetOpLogDB(db *gorm.DB) {
	if db == nil {
		s.opLogWriter = nil
		return
	}
	s.opLogWriter = &dbOpLogWriter{db: db}
}

// SetOpLogWriter 注入自定义操作日志写入方（BUG-077）。
//
// 适用场景：不想落 MySQL 的 sys_operation_log —— 例如 heims 需要写 MongoDB
// 并携带前后快照：
//
//	svc.SetOpLogWriter(myMongoOpLogWriter{})
//
// 传 nil 等价于未注入。
func (s *GenericService[M]) SetOpLogWriter(w OpLogWriter) {
	s.opLogWriter = w
}

// opLogReady 判定操作日志是否具备写入条件。
// 未注入写入方时，EnableOpLog 只是「声明了但没接线」——由构造期告警提示（BUG-077）。
func (s *GenericService[M]) opLogReady() bool {
	return s.config.EnableOpLog && s.opLogWriter != nil
}

// warnOpLogMisconfigured 构造期一次性告警：声明开启审计但没有任何写入方（BUG-077）。
//
// 旧行为的失败模式是「配置写了、开关点了、记录零条、错误零行」，
// 排查成本极高；这里改为启动即打印明确日志（含 EntityName 与两种修法）。
func (s *GenericService[M]) warnOpLogMisconfigured() {
	if !s.config.EnableOpLog || s.opLogWriter != nil {
		return
	}
	name := s.config.EntityName
	if name == "" {
		name = "<未设置 EntityName>"
	}
	logrus.Warnf(
		"gocrux: 实体 %q 的 Config.EnableOpLog=true 但未注入操作日志写入方，"+
			"审计记录将被跳过。请二选一：svc.SetOpLogDB(db)（落内置 sys_operation_log 表）"+
			"或 svc.SetOpLogWriter(w)（自定义落库，如 MongoDB）；若不审计请显式设 EnableOpLog=false",
		name,
	)
}

// writeOpLogRecords 统一写入口：记录失败日志，不向上冒泡
// （审计日志失败不应让主业务失败，但绝不能静默 —— BUG-077 §2.3）。
func (s *GenericService[M]) writeOpLogRecords(ctx context.Context, records []OpLogRecord) {
	if !s.opLogReady() || len(records) == 0 {
		return
	}
	if err := s.opLogWriter.WriteOpLogs(ctx, records); err != nil {
		op := records[0].Operation
		ids := make([]string, 0, len(records))
		for _, r := range records {
			ids = append(ids, r.EntityID)
		}
		logrus.Errorf("gocrux: 写操作日志失败（entity_type=%s operation=%s entity_id=%v count=%d）: %v",
			s.config.EntityName, op, ids, len(records), err)
	}
}

// makeOpLogRecord 构造单条记录（补全 entity_type / operator / request_id / 时间）。
func (s *GenericService[M]) makeOpLogRecord(ctx context.Context, entityID, operation string) OpLogRecord {
	return OpLogRecord{
		EntityType:   s.config.EntityName,
		EntityID:     entityID,
		Operation:    operation,
		OperatorULID: GetUserULID(ctx),
		RequestID:    GetRequestID(ctx),
		OperatedAt:   time.Now(),
	}
}

// snapshotJSON 尽力序列化快照；失败返回 nil（不阻塞主流程）。
func snapshotJSON(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}
