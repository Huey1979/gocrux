package handler

import (
	"context"
	"time"

	"github.com/Huey1979/gocrux/internal/logger"
	"github.com/Huey1979/gocrux/service"

	"github.com/sirupsen/logrus"
)

// traceNode 在管线节点记录结构化日志。用于排查多层级联/长链路请求。
// ctx 中如果有 request_id（service.CtxKeyRequestID），自动附加到日志字段。
func traceNode(ctx context.Context, node string, fields logrus.Fields) {
	if logger.BusinessLog == nil {
		return
	}
	if fields == nil {
		fields = logrus.Fields{}
	}
	fields["node"] = node
	if rid := service.GetRequestID(ctx); rid != "" {
		fields["log_id"] = rid
	}
	logger.BusinessLog.WithFields(fields).Info("TRACE")
}

// traceStart 管线入口日志，返回 start time 供 traceEnd 计算耗时（ms）。
func traceStart(ctx context.Context, node string, fields logrus.Fields) time.Time {
	traceNode(ctx, node+".start", fields)
	return time.Now()
}

// traceEnd 管线出口日志，自动计算耗时。err 为管线返回的 error（nil=成功）。
func traceEnd(ctx context.Context, node string, start time.Time, err error) {
	f := logrus.Fields{"elapsed_ms": time.Since(start).Milliseconds()}
	if err != nil {
		f["error"] = err.Error()
	}
	traceNode(ctx, node+".end", f)
}
