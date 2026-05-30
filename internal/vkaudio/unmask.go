package vkaudio

import (
	"math/big"
	"strconv"
	"strings"
)

const vkAudioAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN0PQRSTUVWXYZO123456789+/="

func unmaskVKAudioURL(raw string, vkID int) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.Contains(raw, "audio_api_unavailable") || !strings.Contains(raw, "?extra=") {
		return raw
	}
	extra := strings.SplitN(raw, "?extra=", 2)[1]
	parts := strings.SplitN(extra, "#", 2)
	decoded := vkAudioB64(parts[0])
	if decoded == "" {
		return raw
	}
	opsRaw := ""
	if len(parts) > 1 && parts[1] != "" {
		opsRaw = vkAudioB64(parts[1])
	}
	if opsRaw == "" {
		return decoded
	}
	ops := strings.Split(opsRaw, "\t")
	for i := len(ops) - 1; i >= 0; i-- {
		step := strings.Split(ops[i], "\v")
		if len(step) == 0 || step[0] == "" {
			return raw
		}
		next, ok := applyVKAudioMaskOp(decoded, step[0], step[1:], vkID)
		if !ok {
			return raw
		}
		decoded = next
	}
	if strings.HasPrefix(decoded, "http") {
		return decoded
	}
	return raw
}

func vkAudioB64(s string) string {
	if s == "" || len(s)%4 == 1 {
		return ""
	}
	t, o := 0, 0
	var b strings.Builder
	for _, ch := range s {
		idx := strings.IndexRune(vkAudioAlphabet, ch)
		if idx < 0 {
			continue
		}
		if o%4 != 0 {
			t = 64*t + idx
		} else {
			t = idx
		}
		prev := o % 4
		o++
		if prev != 0 {
			b.WriteByte(byte(255 & (t >> ((-2 * o) & 6))))
		}
	}
	return b.String()
}

func applyVKAudioMaskOp(s, name string, args []string, vkID int) (string, bool) {
	switch name {
	case "v":
		return reverseString(s), true
	case "r":
		if len(args) < 1 {
			return "", false
		}
		shift, err := strconv.Atoi(args[0])
		if err != nil {
			return "", false
		}
		return rotateAlphabet(s, shift), true
	case "s":
		if len(args) < 1 {
			return "", false
		}
		n, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return "", false
		}
		return shuffleString(s, big.NewInt(n)), true
	case "i":
		if len(args) < 1 {
			return "", false
		}
		n, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return "", false
		}
		key := big.NewInt(n)
		key.Xor(key, big.NewInt(int64(vkID)))
		return shuffleString(s, key), true
	case "x":
		if len(args) < 1 || args[0] == "" {
			return "", false
		}
		key := args[0][0]
		buf := []byte(s)
		for i := range buf {
			buf[i] ^= key
		}
		return string(buf), true
	default:
		return "", false
	}
}

func reverseString(s string) string {
	b := []byte(s)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}

func rotateAlphabet(s string, shift int) string {
	alphabet := vkAudioAlphabet + vkAudioAlphabet
	buf := []byte(s)
	for i := len(buf) - 1; i >= 0; i-- {
		idx := strings.IndexByte(alphabet, buf[i])
		if idx >= 0 {
			pos := idx - shift
			for pos < 0 {
				pos += len(vkAudioAlphabet)
			}
			buf[i] = alphabet[pos]
		}
	}
	return string(buf)
}

func shuffleString(s string, key *big.Int) string {
	if len(s) == 0 {
		return s
	}
	indexes := shuffleIndexes(len(s), key)
	buf := []byte(s)
	for r := 1; r < len(buf); r++ {
		j := indexes[len(buf)-1-r]
		buf[r], buf[j] = buf[j], buf[r]
	}
	return string(buf)
}

func shuffleIndexes(length int, key *big.Int) []int {
	out := make([]int, length)
	if length == 0 {
		return out
	}
	abs := new(big.Int).Abs(new(big.Int).Set(key))
	if abs.BitLen() > 53 {
		mod := big.NewInt(int64(length))
		cur := abs
		for r := length - 1; r >= 0; r-- {
			left := new(big.Int).Mul(big.NewInt(int64(length*(r+1))), cur)
			right := new(big.Int).Add(cur, big.NewInt(int64(r)))
			cur = new(big.Int).Xor(left, right)
			cur.Mod(cur, mod)
			out[r] = int(cur.Int64())
		}
		return out
	}
	t := int(abs.Int64())
	for e := length - 1; e >= 0; e-- {
		t = (length*(e+1) ^ (t + e)) % length
		out[e] = t
	}
	return out
}
