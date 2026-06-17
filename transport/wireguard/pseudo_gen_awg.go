//go:build with_awg

// Pseudo names / hosts / public IPs for masquerade decoys.
//
// Purpose — plausibility WITHOUT recognizability. A junk decoy (e.g. the SIP
// INVITE template) needs pronounceable user/host strings that are NOT hardcoded
// RFC beacons (`bob@biloxi.com` is a public marker a DPI knows), not obvious
// garbage (`7f3a9c`), and not private IPs (which give the forgery away).
//
// Each value is drawn from crypto/rand (not seeded → no cross-user signature).
// This is not cryptographic (it's junk, not a secret); crypto/rand is used only
// so the sequence is unpredictable between users.
//
// Ported from the LxBox-side PseudoGen (§127).
package wireguard

import (
	"crypto/rand"
	"math/big"
	"strings"
)

// consonants without q/x/y (unpronounceable combos).
var pgCons = []string{
	"b", "c", "d", "f", "g", "h", "j", "k", "l", "m",
	"n", "p", "r", "s", "t", "v", "w", "z",
}

var pgVow = []string{"a", "e", "i", "o", "u"}

// onset clusters (before a vowel) — pronounceable syllable onsets.
var pgOnset = []string{
	"br", "tr", "cr", "dr", "fr", "gr", "pr", "st", "sp", "sk",
	"sl", "sm", "sn", "bl", "cl", "fl", "gl", "pl", "tw", "sh",
}

// coda clusters (after a vowel, word-final) — pronounceable codas:
// silk/bank/song. "lk" is coda-only, never an onset.
var pgCoda = []string{
	"lk", "sk", "st", "nk", "sh", "nt", "rk", "ng",
}

var pgTLD = []string{"com", "net", "org", "io", "co"}

// pgIntn returns a uniform int in [0, n) from crypto/rand (panics only on a
// broken RNG, which is unrecoverable anyway).
func pgIntn(n int) int {
	if n <= 1 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		panic("amneziawg: crypto/rand failed in PseudoGen")
	}
	return int(v.Int64())
}

func pgPick(seq []string) string { return seq[pgIntn(len(seq))] }

// pgChance reports true with probability pct%.
func pgChance(pct int) bool { return pgIntn(100) < pct }

// pgWord builds a pronounceable syllabic word (looks like a real name):
//
//	word = C + V + [ (60%: C | 40%: ONSET) + V ] × {2..3} + (50%: tail)
//	tail = 30%: CODA | 70%: C
func pgWord() string {
	var b strings.Builder
	b.WriteString(pgPick(pgCons)) // C
	b.WriteString(pgPick(pgVow))  // V
	blocks := 2 + pgIntn(2)       // 2..3
	for i := 0; i < blocks; i++ {
		if pgChance(60) {
			b.WriteString(pgPick(pgCons))
		} else {
			b.WriteString(pgPick(pgOnset))
		}
		b.WriteString(pgPick(pgVow)) // V
	}
	if pgChance(50) {
		if pgChance(30) {
			b.WriteString(pgPick(pgCoda))
		} else {
			b.WriteString(pgPick(pgCons))
		}
	}
	return b.String()
}

// pgUser returns a pseudo-username: a word, 30% of the time joined as word_word.
func pgUser() string {
	u := pgWord()
	if pgChance(30) {
		return u + "_" + pgWord()
	}
	return u
}

// pgIsReservedIP reports whether a.b.x.x is reserved/private (unfit as a public
// VoIP host — it would expose the forgery).
func pgIsReservedIP(a, b int) bool {
	switch {
	case a == 0 || a == 10 || a == 127:
		return true
	case a == 172 && b >= 16 && b <= 31:
		return true
	case a == 192 && b == 168:
		return true
	case a == 169 && b == 254:
		return true
	case a == 100 && b >= 64 && b <= 127: // CGNAT
		return true
	case a >= 224: // multicast/reserved
		return true
	}
	return false
}

// pgPublicIP returns a public IPv4 (private/reserved ranges excluded).
func pgPublicIP() string {
	for {
		a := 1 + pgIntn(223) // 1..223
		b := pgIntn(256)
		if pgIsReservedIP(a, b) {
			continue
		}
		c := pgIntn(256)
		d := 1 + pgIntn(254) // 1..254
		return itoa(a) + "." + itoa(b) + "." + itoa(c) + "." + itoa(d)
	}
}

// pgHost returns a pseudo SIP host: a 2-/3-level domain, sip subdomain, or a
// public IP. Shares: 30% 3-level / 30% public IP / 30% 2-level / 10% sip-sub.
func pgHost() string {
	switch r := pgIntn(100); {
	case r < 30:
		return pgWord() + "." + pgWord() + "." + pgPick(pgTLD) // 3-level
	case r < 60:
		return pgPublicIP() // IP
	case r < 90:
		return pgWord() + "." + pgPick(pgTLD) // 2-level
	default:
		return "sip." + pgWord() + "." + pgPick(pgTLD) // sip subdomain
	}
}

// itoa is a tiny non-allocating-ish base-10 itoa for the IP octets (avoids a
// strconv import for three call sites).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [3]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
