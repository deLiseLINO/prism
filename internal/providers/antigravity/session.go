package antigravity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/big"
)

func SessionID(threadAnchor string, firstUserText string) string {
	text := firstUserText
	if threadAnchor != "" {
		text = "codex-thread:" + threadAnchor
	}
	if text == "" {
		n, err := rand.Int(rand.Reader, big.NewInt(9_000_000_000_000_000_000))
		if err != nil {
			return "-0"
		}
		return "-" + fmt.Sprintf("%d", n.Int64())
	}
	digest := sha256.Sum256([]byte(text))
	masked := binary.BigEndian.Uint64(digest[:8]) & 0x7fffffffffffffff
	return "-" + fmt.Sprintf("%d", masked)
}

func NewRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return requestIDPrefix + "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return requestIDPrefix + fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
