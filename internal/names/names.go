package names

import (
	"crypto/rand"
	"encoding/hex"
	"math/big"
	"regexp"
)

var adjectives = []string{
	"agil", "alto", "amplo", "atento", "bravo", "breve", "calmo", "claro", "doce", "eterno",
	"firme", "forte", "gentil", "lento", "leve", "livre", "lucido", "manso", "nobre", "novo",
	"pleno", "pronto", "quieto", "rapido", "raro", "sereno", "sutil", "terno", "vasto", "vivo",
}

var nouns = []string{
	"cedro", "farol", "rio", "vale", "monte", "porto", "trigo", "milho", "orvalho", "ipe",
	"jatoba", "caju", "mangue", "serra", "lago", "cais", "brisa", "pedra", "areia", "duna",
	"vento", "ninho", "ponte", "trilha", "campo", "bosque", "atol", "recife", "delta", "cerrado",
}

var validName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)

func Valid(name string) bool { return validName.MatchString(name) }

func pick(list []string) string {
	n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(list))))
	return list[n.Int64()]
}

func Random() string {
	return pick(adjectives) + "_" + pick(nouns)
}

func ID() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}
