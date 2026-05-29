package logger

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	errs "github.com/Huey1979/gocrux/errors"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// RequestIDKey 请求 ID 在上下文中的 key
const RequestIDKey = "log_id"

var (
	// RequestLog 请求日志（URL、GET/POST数据）
	RequestLog *logrus.Logger
	// ResponseLog 响应日志（返回数据）
	ResponseLog *logrus.Logger
	// BusinessLog 业务日志（MySQL查询、业务逻辑节点）
	BusinessLog *logrus.Logger
)

// Init 初始化日志系统，创建 logs 目录及三个独立日志实例
func Init(logDir string) error {
	if logDir == "" {
		logDir = "./logs"
	}
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return errs.ErrLoggerCreateDir(err)
	}

	RequestLog = newDailyLogger(filepath.Join(logDir, "request"))
	ResponseLog = newDailyLogger(filepath.Join(logDir, "response"))
	BusinessLog = newDailyLogger(filepath.Join(logDir, "business"))
	return nil
}

// newDailyLogger 创建按天滚动的 logrus.Logger
func newDailyLogger(filePrefix string) *logrus.Logger {
	l := logrus.New()
	l.SetLevel(logrus.DebugLevel)
	l.SetFormatter(&logrus.TextFormatter{
		TimestampFormat: "2006-01-02 15:04:05",
		DisableColors:   true,
		FullTimestamp:   true,
	})
	l.SetOutput(&dailyWriter{prefix: filePrefix})
	return l
}

// dailyWriter 按天滚动 writer，每日自动创建新文件
type dailyWriter struct {
	prefix  string
	mu      sync.Mutex
	file    *os.File
	curDate string
}

func (w *dailyWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	today := time.Now().Format("2006-01-02")
	if w.curDate != today {
		if w.file != nil {
			w.file.Close()
		}
		filename := fmt.Sprintf("%s_%s.log", w.prefix, today)
		f, err := os.OpenFile(filename, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return 0, err
		}
		w.file = f
		w.curDate = today
	}

	if w.file == nil {
		return len(p), nil
	}
	return w.file.Write(p)
}

// GenerateRequestID 生成请求唯一随机码（16位十六进制，碰撞概率极低）
func GenerateRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// LogRequest 记录请求信息（URL、GET参数、POST body）
// 在中间件中调用，发生在业务逻辑之前
func LogRequest(c *gin.Context, requestID string) {
	// 读取并恢复请求 Body
	bodyBytes, _ := io.ReadAll(c.Request.Body)
	c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	fields := logrus.Fields{
		"log_id":    requestID,
		"method":    c.Request.Method,
		"url":       c.Request.URL.String(),
		"client_ip": c.ClientIP(),
	}

	// GET 参数
	rawQuery := c.Request.URL.RawQuery
	if rawQuery != "" {
		fields["query"] = rawQuery
	}

	// POST/PUT body（截断过长内容）
	if len(bodyBytes) > 0 {
		body := string(bodyBytes)
		if len(body) > 2048 {
			body = body[:2048] + fmt.Sprintf("...(truncated, total %d bytes)", len(bodyBytes))
		}
		fields["body"] = body
	}

	RequestLog.WithFields(fields).Info("REQUEST")
}

// LogResponse 记录响应信息（状态码、返回数据）
// 在中间件中调用，发生在业务逻辑之后
func LogResponse(c *gin.Context, requestID string, status int, respBody string) {
	fields := logrus.Fields{
		"log_id": requestID,
		"method": c.Request.Method,
		"url":    c.Request.URL.String(),
		"status": status,
	}

	if len(respBody) > 0 {
		if len(respBody) > 2048 {
			respBody = respBody[:2048] + fmt.Sprintf("...(truncated, total %d bytes)", len(respBody))
		}
		fields["body"] = respBody
	}

	ResponseLog.WithFields(fields).Info("RESPONSE")
}

// LogBusiness 记录业务逻辑重要节点
// node: 业务节点名称，如 "dept.create", "site.publish", "login.success"
// fields: 附加字段，会自动附加请求ID
func LogBusiness(c *gin.Context, node string, fields logrus.Fields) {
	if fields == nil {
		fields = logrus.Fields{}
	}
	fields["node"] = node
	if c != nil {
		if id, exists := c.Get(RequestIDKey); exists {
			fields["log_id"] = id
		}
	}
	BusinessLog.WithFields(fields).Info("BUSINESS")
}

// LogBusinessSimple 记录业务逻辑重要节点（无额外字段）
func LogBusinessSimple(c *gin.Context, node string) {
	LogBusiness(c, node, nil)
}
