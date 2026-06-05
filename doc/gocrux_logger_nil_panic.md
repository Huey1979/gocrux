# gocrux Bug Report：未初始化的 Logger 导致 error-handling 路径 nil panic

## 严重程度

**高** — 任何触发 `InternalError` 的请求都会导致服务端 nil pointer panic，返回 500，且原始错误信息被完全掩盖，排查极其困难。

---

## 问题位置

### 1. 变量定义（未初始化）

**文件**: `internal/logger/logger.go` 第 22-29 行

```go
var (
    RequestLog  *logrus.Logger  // ← 默认 nil
    ResponseLog *logrus.Logger  // ← 默认 nil
    BusinessLog *logrus.Logger  // ← 默认 nil
)
```

这 3 个包级变量只有在调用 `logger.Init(logDir)` 后才会被赋值（第 32-43 行）。如果使用者自己管理日志系统、或者根本没调用 `Init()`，它们永远是 nil。

### 2. 崩溃触发点

**文件**: `handler/response.go` 第 98 行，`InternalError()` 函数内

```go
func InternalError(c *gin.Context, err error) {
    // ... logrus 直接记录（这个没事，用的是全局 logrus）

    // ↓↓↓ 这里崩溃 ↓↓↓
    logger.LogBusiness(c, "internal_error", logrus.Fields{
        "error":  err.Error(),
        "caller": fmt.Sprintf("%s:%d", file, line),
    })
    // ↑↑↑ BusinessLog 是 nil → nil.WithFields() → panic ↑↑↑
}
```

### 3. 三个日志函数都没有 nil 保护

**文件**: `internal/logger/logger.go`

| 函数 | 行号 | 问题 |
|------|------|------|
| `LogRequest` | 100-131 | 第 130 行 `RequestLog.WithFields(...)` 没有 nil 检查 |
| `LogResponse` | 133-154 | 第 153 行 `ResponseLog.WithFields(...)` 没有 nil 检查 |
| `LogBusiness` | 156-173 | 第 172 行 `BusinessLog.WithFields(...)` 没有 nil 检查 |

---

## 崩溃链路（完整复现过程）

```
1. 任意错误发生（如参数校验失败、DB 错误等）
      ↓
2. handleError(c, err)  
      ↓
3. InternalError(c, err)       // response.go:86
      ↓
4. logger.LogBusiness(c, ...)  // response.go:98
      ↓
5. BusinessLog.WithFields(...) // logger.go:172, BusinessLog == nil
      ↓
6. 💥 nil pointer panic
      ↓
7. Recovery 中间件捕获 panic
      ↓
8. Recovery 内调用 InternalError → 又回到步骤 3 → 二次 panic 💥
      ↓
9. 用户看到：{"code": 500, "msg": "系统发生错误"}
   （没有任何有用信息，日志里也找不到 PANIC 记录——因为 logrus 的日志写进了文件，
    但 Recovery 日志里的 logrus.WithFields 又触发了 nil panic，所以啥都没记下来）
```

---

## 为什么说是框架问题

1. **框架定义了 API，但没有保证 API 安全**——包级变量没给默认值，调用方也没做 nil 检查，相当于给使用者埋了一个雷。

2. **错误处理路径产生了新错误**——`InternalError` 本身是错误兜底函数，结果它自己会 panic。错误处理代码应该是防御性最强的代码，绝不能再产生 panic。

3. **文档缺失**——没有任何文档说明 "必须调用 `logger.Init()` 否则会 crash"。使用者自己管理日志系统（不调 `Init()`）是完全合理的场景。

4. **`Init()` 不是强制入口**——框架没有在启动流程中强制调用 `Init()`，也没有用 `init()` 函数给默认值。

---

## 推荐修复方案

### 方案一：加 nil 检查（推荐，最小改动）

**文件**: `internal/logger/logger.go`

在每个日志函数的开头加 nil guard：

```go
func LogRequest(c *gin.Context, requestID string) {
    if RequestLog == nil {
        return   // ← 加这一行
    }
    // ... 原有逻辑
}

func LogResponse(c *gin.Context, requestID string, status int, respBody string) {
    if ResponseLog == nil {
        return   // ← 加这一行
    }
    // ... 原有逻辑
}

func LogBusiness(c *gin.Context, node string, fields logrus.Fields) {
    if BusinessLog == nil {
        return   // ← 加这一行
    }
    // ... 原有逻辑
}
```

**优点**: 改动最小、不会 break 任何现有代码、日志降级不崩溃  
**缺点**: 无

### 方案二：给默认 Logger

```go
func init() {
    // 默认输出到 stderr，防止 nil panic
    if RequestLog == nil {
        RequestLog = logrus.New()
    }
    if ResponseLog == nil {
        ResponseLog = logrus.New()
    }
    if BusinessLog == nil {
        BusinessLog = logrus.New()
    }
}
```

**优点**: 不调 `Init()` 也能看到日志（输出到 stderr）  
**缺点**: 如果使用者真的不需要日志，会产生不必要的输出

### 方案三：两者都做（最安全）

先 `init()` 给默认值，再在各函数里加 nil 判断（双重保险）。

---

## 验证方式

在不调用 `logger.Init()` 的情况下启动服务，发送一个会触发 `InternalError` 的请求（如非法参数），确认：

- **修复前**: 返回 500，服务端 nil panic
- **修复后**: 正常返回 JSON 错误响应，不 panic

---

## 附加问题（优先级较低）

### `InternalError` 中同时用了两套日志

`handler/response.go` 第 91-101 行：

```go
// 第 91-95 行：直接用 logrus 全局 logger（这个不会 crash）
logrus.WithFields(logrus.Fields{
    "log_id": requestID,
    "error":  err.Error(),
    "caller": fmt.Sprintf("%s:%d", file, line),
}).Error("内部错误")

// 第 98-101 行：又用框架自己的 logger.LogBusiness（这个会 crash）
logger.LogBusiness(c, "internal_error", logrus.Fields{
    "error":  err.Error(),
    "caller": fmt.Sprintf("%s:%d", file, line),
})
```

同一条错误记了两遍，建议评估是否只需要一处。`LogBusiness` 走的是框架的日志体系（按天滚动），`logrus.WithFields` 走的是全局 logrus（行为取决于使用者配置），两者语义不同，可能需要保留，但至少应该保证 LogBusiness 不会 panic。
