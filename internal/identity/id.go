// Package identity 生成网关内部的不可猜测、按时间近似有序的标识符。
package identity

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

var idEncoding = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

func New(prefix string) (string, error) {
	var value [16]byte
	milliseconds := uint64(time.Now().UnixMilli())
	value[0] = byte(milliseconds >> 40)
	value[1] = byte(milliseconds >> 32)
	value[2] = byte(milliseconds >> 24)
	value[3] = byte(milliseconds >> 16)
	value[4] = byte(milliseconds >> 8)
	value[5] = byte(milliseconds)
	if _, err := rand.Read(value[6:]); err != nil {
		return "", fmt.Errorf("生成随机 id: %w", err)
	}
	encoded := idEncoding.EncodeToString(value[:])
	return prefix + strings.ToLower(encoded), nil
}

func Timestamp(id string) (time.Time, error) {
	separator := strings.IndexByte(id, '_')
	if separator < 0 || separator == len(id)-1 {
		return time.Time{}, fmt.Errorf("id 格式无效")
	}
	decoded, err := idEncoding.DecodeString(strings.ToUpper(id[separator+1:]))
	if err != nil || len(decoded) != 16 {
		return time.Time{}, fmt.Errorf("id 编码无效")
	}
	var padded [8]byte
	copy(padded[2:], decoded[:6])
	milliseconds := binary.BigEndian.Uint64(padded[:])
	return time.UnixMilli(int64(milliseconds)).UTC(), nil
}
