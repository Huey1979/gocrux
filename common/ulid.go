package common

import (
	"crypto/md5"
	crand "crypto/rand"
	"encoding/hex"
	"time"
)

// ULID 生成器
// ULID = 26字符 = 10字符时间戳 + 16字符随机数
//
// 【并发安全】随机数取自 crypto/rand（系统 CSPRNG，内部并发安全），
// 不使用 math/rand 的手工 Source —— 后者为环形缓冲实现，
// **并发调用会撕裂内部索引并 panic**（index out of range [-1]）。
// 详见 ulid_test.go 的 TestNewULIDConcurrentUniqueness。

const (
	ULIDTimestampLen = 10
	ULIDRandomLen    = 16
	ULIDTotalLen     = ULIDTimestampLen + ULIDRandomLen
)

// encodeChars 字符集（Crockford Base32）：
// 去掉易混淆字符 0/O/1/I/L/U，保证人眼可读与转录准确。
var encodeChars = []byte("0123456789ABCDEFGHJKMNPQRSTVWXYZ")

// NewULID 生成新的 ULID（并发安全）。
//
// 组成：10 字符毫秒时间戳（base32）+ 16 字符随机数（crypto/rand）。
// 时间戳段使 ULID 天然按生成时间有序；随机段保证唯一性。
//
// 唯一性说明：碰撞需「同一毫秒 + 16 字符随机相同」，概率约 32^-16 ≈ 1.2e-24。
// 调用方**不需要**为了防冲突去查库 —— 框架层面把它当作不会发生，
// 万一发生（INSERT 1062）由事务整体回滚 + 重试兜底。
func NewULID() string {
	timestamp := time.Now().UnixMilli()
	encoded := encodeUint64(uint64(timestamp), ULIDTimestampLen)
	return encoded + randomChars(ULIDRandomLen)
}

// randomChars 生成 n 个随机字符（取自 encodeChars 字符集）。
//
// 用 crypto/rand 一次性读 n 字节再逐字节映射到字符集：
//   - 相比「每字符一次 syscall」减少系统调用次数；
//   - 相比 math/rand 无并发安全问题。
//
// 映射采用 % len(encodeChars)：n 字节的取值范围是 0-255，
// 32 整除 256，因此**分布均匀、无模偏差**（这是选择 32 字符集的好处之一）。
func randomChars(n int) string {
	buf := make([]byte, n)
	if _, err := crand.Read(buf); err != nil {
		// crypto/rand.Read 在受支持的平台上不会失败（Go 1.24+ 失败即 panic）。
		// 万一失败（如极端的 fd 耗尽），退回时间戳纳秒混合，保证不 panic、
		// 且仍具备可用随机性 —— 主键服务不可因随机源抖动而中断。
		return fallbackChars(n)
	}
	out := make([]byte, n)
	for i, b := range buf {
		out[i] = encodeChars[int(b)%len(encodeChars)]
	}
	return string(out)
}

// fallbackChars 随机源不可用时的降级实现（时间戳纳秒 + 单调计数混合）。
//
// 仅用于 crypto/rand 读取失败的极端场景。不追求密码学强度，
// 只要求「不 panic + 同一毫秒内实际不同」。
func fallbackChars(n int) string {
	out := make([]byte, n)
	seed := uint64(time.Now().UnixNano())
	for i := 0; i < n; i++ {
		// xorshift64*：无状态依赖，纯局部变量，天然并发安全
		seed ^= seed << 13
		seed ^= seed >> 7
		seed ^= seed << 17
		out[i] = encodeChars[seed%uint64(len(encodeChars))]
	}
	return string(out)
}

// encodeUint64 将数字编码为指定长度的 base32 字符串
func encodeUint64(v uint64, length int) string {
	result := make([]byte, length)
	for i := length - 1; i >= 0; i-- {
		result[i] = encodeChars[v%32]
		v = v / 32
	}
	return string(result)
}

// ConversationID 生成两人会话ID (MD5)
func ConversationID(ulidA, ulidB string) string {
	// 排序
	if ulidA > ulidB {
		ulidA, ulidB = ulidB, ulidA
	}

	// 拼接并计算 MD5
	h := md5.New()
	h.Write([]byte(ulidA + ulidB))
	return hex.EncodeToString(h.Sum(nil))
}

// InitULID 若 field 为空字符串则自动生成 ULID。
// 用于简化 entity.SetID() 实现，替换重复的 if/NewULID 模板。
//
// 使用示例：
//
//	func (s *SysForm) SetID() {
//	    common.InitULID(&s.FormULID)
//	}
//
// 注意「非空则不覆盖」这一语义与 service._beforeCreate 的
// 「PK 为空才补 ULID」保持一致 —— 预分配接入后二者必须同口径，
// 否则会出现「预分配的 ULID 被重新生成」。
func InitULID(field *string) {
	if *field == "" {
		*field = NewULID()
	}
}

// PasswordHash 密码哈希
func PasswordHash(password, salt string) string {
	h := md5.New()
	h.Write([]byte(password + salt))
	return hex.EncodeToString(h.Sum(nil))
}
