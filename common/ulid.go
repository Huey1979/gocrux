package common

import (
	"crypto/md5"
	"encoding/hex"
	"math/rand"
	"time"
)

// ULID 生成器
// ULID = 26字符 = 10字符时间戳 + 16字符随机数

const (
	ULIDTimestampLen = 10
	ULIDRandomLen    = 16
	ULIDTotalLen     = ULIDTimestampLen + ULIDRandomLen
)

var (
	// 字符集
	encodeChars = []byte("0123456789ABCDEFGHJKMNPQRSTVWXYZ") // 避免 0,O,1,I,L 等易混淆字符
	randSrc     = rand.NewSource(time.Now().UnixNano())
)

// NewULID 生成新的 ULID
func NewULID() string {
	// 当前时间戳（base32 编码，10位）
	timestamp := time.Now().UnixMilli()
	encoded := encodeUint64(uint64(timestamp), ULIDTimestampLen)

	// 随机数（16位）
	random := make([]byte, ULIDRandomLen)
	for i := 0; i < ULIDRandomLen; i++ {
		random[i] = encodeChars[randSrc.Int63()%int64(len(encodeChars))]
	}

	return encoded + string(random)
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

// PasswordHash 密码哈希
func PasswordHash(password, salt string) string {
	h := md5.New()
	h.Write([]byte(password + salt))
	return hex.EncodeToString(h.Sum(nil))
}
