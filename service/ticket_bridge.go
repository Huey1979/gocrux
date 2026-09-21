package service

import "context"

// ============================================================
// 预分配凭证校验桥（service 侧）
//
// 为什么是"桥"而不是直接依赖 handler：
//
//	Go 的包依赖是单向的 —— handler 导入 service，因此 service **不能**导入
//	handler。但凭证的真值（请求级注册表）恰恰归 handler 所有。
//
// 解决方式：handler 在请求入口把一个**校验函数**挂到 ctx 上，service 落库时
// 取出并调用。这样：
//   - service 不需要知道注册表的结构（解耦）；
//   - 不产生反向包依赖；
//   - 校验逻辑仍只有一份（在 handler 侧）。
//
// 语义（设计文档 §3.2）：
//
//	PK 非空 且 存在于本请求 ctx 的预分配注册表 → 框架生成的，信任，直接落库
//	否则                                      → 外部传入的，按原语义处理
//
// **向后兼容**：ctx 上没有桥时返回 true（视为可信）—— 那说明调用方没走
// 预分配通道（老代码 / 直接调 service），保持既有的"非空即信任"语义。
// ============================================================

// PKTrustChecker 凭证校验函数签名（由 handler 侧挂载）。
//
// 返回 true 表示该 PK 可信（框架预分配）；false 表示外部传入。
type PKTrustChecker func(ulid string) bool

// ctxKeyPKTrustChecker 凭证校验桥的 context key。
type ctxKeyPKTrustChecker struct{}

// WithPKTrustChecker 把凭证校验桥挂到 ctx（由 handler 调用）。
func WithPKTrustChecker(ctx context.Context, fn PKTrustChecker) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyPKTrustChecker{}, fn)
}

// pkTrustCheckerFrom 取凭证校验桥（未挂载返回 nil）。
func pkTrustCheckerFrom(ctx context.Context) PKTrustChecker {
	fn, _ := ctx.Value(ctxKeyPKTrustChecker{}).(PKTrustChecker)
	return fn
}

// verifyPKTrusted 校验一个非空 PK 是否可作为"框架生成"信任。
//
// 未挂载桥 → true（非预分配通道，保持既有语义，向后兼容的关键）。
func verifyPKTrusted(ctx context.Context, ulid string) bool {
	if ulid == "" {
		return false
	}
	fn := pkTrustCheckerFrom(ctx)
	if fn == nil {
		return true
	}
	return fn(ulid)
}
