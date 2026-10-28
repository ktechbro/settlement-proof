package solana

import (
	"fmt"
	"math/big"
	"strings"
)

const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func Decode58(s string) ([]byte, error) {
	if s == "" || len(s) > 1024 {
		return nil, fmt.Errorf("invalid base58 length")
	}
	n := new(big.Int)
	radix := big.NewInt(58)
	for _, c := range s {
		i := strings.IndexRune(alphabet, c)
		if i < 0 {
			return nil, fmt.Errorf("invalid base58 character")
		}
		n.Mul(n, radix)
		n.Add(n, big.NewInt(int64(i)))
	}
	zeros := 0
	for zeros < len(s) && s[zeros] == '1' {
		zeros++
	}
	return append(make([]byte, zeros), n.Bytes()...), nil
}

func Encode58(b []byte) string {
	zeros := 0
	for zeros < len(b) && b[zeros] == 0 {
		zeros++
	}
	n := new(big.Int).SetBytes(b)
	radix := big.NewInt(58)
	mod := new(big.Int)
	out := []byte{}
	for n.Sign() > 0 {
		n.QuoRem(n, radix, mod)
		out = append(out, alphabet[mod.Int64()])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return strings.Repeat("1", zeros) + string(out)
}

func Valid58(s string, size int) bool {
	if len(s) > size*2 {
		return false
	}
	b, err := Decode58(s)
	return err == nil && len(b) == size && Encode58(b) == s
}
