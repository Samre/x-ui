package random

import (
	"crypto/rand"
	mrand "math/rand"
)

var numSeq [10]rune
var lowerSeq [26]rune
var upperSeq [26]rune
var numLowerSeq [36]rune
var numUpperSeq [36]rune
var allSeq [62]rune

func init() {
	for i := 0; i < 10; i++ {
		numSeq[i] = rune('0' + i)
	}
	for i := 0; i < 26; i++ {
		lowerSeq[i] = rune('a' + i)
		upperSeq[i] = rune('A' + i)
	}

	copy(numLowerSeq[:], numSeq[:])
	copy(numLowerSeq[len(numSeq):], lowerSeq[:])

	copy(numUpperSeq[:], numSeq[:])
	copy(numUpperSeq[len(numSeq):], upperSeq[:])

	copy(allSeq[:], numSeq[:])
	copy(allSeq[len(numSeq):], lowerSeq[:])
	copy(allSeq[len(numSeq)+len(lowerSeq):], upperSeq[:])
}

func Seq(n int) string {
	runes := make([]rune, n)
	for i := 0; i < n; i++ {
		runes[i] = allSeq[mrand.Intn(len(allSeq))]
	}
	return string(runes)
}

// CryptoSeq 生成 n 个字母数字字符，随机源为 crypto/rand。
// 供会话签名密钥这类安全相关的取值使用：math/rand 的输出可预测，
// 不能拿来当密钥。
func CryptoSeq(n int) string {
	return string(cryptoSeq(n))
}

func cryptoSeq(n int) []rune {
	randomBytes := make([]byte, n)
	if _, err := rand.Read(randomBytes); err != nil {
		// 概率极低（Linux 会阻塞等待熵池，而不是失败）。这里必须中断：
		// 静默退回 math/rand 会让生成的会话密钥弱到可被持久利用。
		panic("crypto/rand read failed: " + err.Error())
	}
	runes := make([]rune, n)
	for i, ch := range randomBytes {
		runes[i] = allSeq[int(ch)%len(allSeq)]
	}
	return runes
}
