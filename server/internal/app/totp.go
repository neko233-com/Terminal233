package app

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

func decodeTOTPSecret(secret string) ([]byte, error) {
	return base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimRight(strings.TrimSpace(secret), "=")))
}
func totpAt(secret []byte, step int64) string {
	var count [8]byte
	binary.BigEndian.PutUint64(count[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	_, _ = mac.Write(count[:])
	hash := mac.Sum(nil)
	offset := hash[len(hash)-1] & 15
	number := (binary.BigEndian.Uint32(hash[offset:offset+4]) & 0x7fffffff) % 1000000
	return fmt.Sprintf("%06d", number)
}
func verifyTOTP(encoded, code string, now time.Time) bool {
	key, err := decodeTOTPSecret(encoded)
	if err != nil || len(key) < 20 || len(code) != 6 {
		return false
	}
	step := now.Unix() / 30
	match := 0
	for offset := int64(-1); offset <= 1; offset++ {
		match |= subtle.ConstantTimeCompare([]byte(totpAt(key, step+offset)), []byte(code))
	}
	return match == 1
}
