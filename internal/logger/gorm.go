package logger

import (
	"context"
	"time"

	gormLogger "gorm.io/gorm/logger"

	"github.com/sirupsen/logrus"
)

// GormLogger 自定义 GORM 日志，将 SQL 查询写入 BusinessLog
type GormLogger struct {
	SlowThreshold time.Duration
	LogLevel      gormLogger.LogLevel
}

// NewGormLogger 创建 GORM 日志实例
// slowThreshold: 慢查询阈值，超过此值的SQL将以WARN级别记录
func NewGormLogger(slowThreshold time.Duration) *GormLogger {
	return &GormLogger{
		SlowThreshold: slowThreshold,
		LogLevel:      gormLogger.Info,
	}
}

// LogMode 设置日志级别
func (l *GormLogger) LogMode(level gormLogger.LogLevel) gormLogger.Interface {
	nl := *l
	nl.LogLevel = level
	return &nl
}

// Info info 级别日志
func (l *GormLogger) Info(ctx context.Context, msg string, data ...interface{}) {
	if l.LogLevel >= gormLogger.Info {
		fields := extractLogFields(ctx)
		BusinessLog.WithFields(fields).Infof("[GORM] "+msg, data...)
	}
}

// Warn warn 级别日志
func (l *GormLogger) Warn(ctx context.Context, msg string, data ...interface{}) {
	if l.LogLevel >= gormLogger.Warn {
		fields := extractLogFields(ctx)
		BusinessLog.WithFields(fields).Warnf("[GORM] "+msg, data...)
	}
}

// Error error 级别日志
func (l *GormLogger) Error(ctx context.Context, msg string, data ...interface{}) {
	if l.LogLevel >= gormLogger.Error {
		fields := extractLogFields(ctx)
		BusinessLog.WithFields(fields).Errorf("[GORM] "+msg, data...)
	}
}

// Trace 记录 SQL 执行（由 GORM 自动调用）
func (l *GormLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if l.LogLevel <= gormLogger.Silent {
		return
	}

	elapsed := time.Since(begin)
	sql, rows := fc()

	fields := logrus.Fields{
		"duration_ms": elapsed.Milliseconds(),
		"rows":        rows,
	}
	if rid, ok := ctx.Value(RequestIDKey).(string); ok && rid != "" {
		fields["log_id"] = rid
	}

	switch {
	case err != nil:
		fields["error"] = err.Error()
		BusinessLog.WithFields(fields).Errorf("[SQL] %s", sql)
	case l.SlowThreshold > 0 && elapsed > l.SlowThreshold:
		fields["slow"] = true
		BusinessLog.WithFields(fields).Warnf("[SQL-SLOW] %s", sql)
	default:
		BusinessLog.WithFields(fields).Infof("[SQL] %s", sql)
	}
}

// extractLogFields 从 context 提取日志公共字段
func extractLogFields(ctx context.Context) logrus.Fields {
	fields := logrus.Fields{}
	if rid, ok := ctx.Value(RequestIDKey).(string); ok && rid != "" {
		fields["log_id"] = rid
	}
	return fields
}
