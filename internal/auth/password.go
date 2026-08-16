package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"

	"golang.org/x/crypto/bcrypt"
)

const bcryptCost = 12

var dummyHash []byte

func init() {
	dummyHash, _ = bcrypt.GenerateFromPassword([]byte("ocr-timing-dummy"), bcryptCost)
}

func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func CheckPassword(hash, password string) bool {
	target := dummyHash
	if strings.TrimSpace(hash) != "" {
		target = []byte(hash)
	}
	return bcrypt.CompareHashAndPassword(target, []byte(password)) == nil && strings.TrimSpace(hash) != ""
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func NewToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func ValidPassword(password string) bool {
	if len(password) < 8 || len(password) > 128 {
		return false
	}
	for _, r := range password {
		if unicode.IsSpace(r) && r != ' ' {
			return false
		}
	}
	return true
}
