//go:build with_awg

package wireguard

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// This file is a faithful, test-only re-implementation of the vendored
// amneziawg-go CPS parser (submodules/wireguard-go/device/obf.go) for the tags
// the masquerade generators emit: <b 0xHEX>, <r N>, <rc N>, <rd N>. It exists so
// the structural tests in masque_awg_test.go can assert the on-wire bytes
// without depending on the submodule's unexported newObfChain.
//
// It mirrors newObfChain semantics deliberately, INCLUDING the scan-for-<...>
// behaviour that silently skips text between tags — so a test never asserts a
// property the real engine does not enforce. The real-engine acceptance check is
// a separate, transient submodule test documented in the IMPLEMENTATION_REPORT;
// see SPECS/TASKS/009 DoD.
//
// chars52 / digits10 match obf_randchars.go / obf_randdigits.go.
const (
	testChars52  = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	testDigits10 = "0123456789"
)

type cpsTag struct {
	kind string // "b", "r", "rc", "rd"
	data []byte // for "b"
	n    int    // for "r"/"rc"/"rd"
}

type testObfChain struct {
	tags []cpsTag
}

// parseCPS parses a CPS spec exactly like newObfChain: scan for <...> tokens,
// skip anything between them, split each token on whitespace, dispatch on the
// keyword. Unknown/empty tags are an error (newObfChain joins such errors).
func parseCPS(spec string) (*testObfChain, error) {
	var (
		tags []cpsTag
		errs []error
	)
	remaining := spec
	for {
		start := strings.IndexByte(remaining, '<')
		if start == -1 {
			break
		}
		end := strings.IndexByte(remaining[start:], '>')
		if end == -1 {
			return nil, errors.New("missing enclosing >")
		}
		end += start
		tag := remaining[start+1 : end]
		remaining = remaining[end+1:]

		parts := strings.Fields(tag)
		if len(parts) == 0 {
			errs = append(errs, errors.New("empty tag"))
			continue
		}
		key := parts[0]
		val := ""
		if len(parts) > 1 {
			val = parts[1]
		}
		switch key {
		case "b":
			v := strings.TrimPrefix(val, "0x")
			if len(v) == 0 {
				errs = append(errs, errors.New("empty <b> argument"))
				continue
			}
			if len(v)%2 != 0 {
				errs = append(errs, errors.New("odd <b> hex length"))
				continue
			}
			data, err := hex.DecodeString(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("bad <b> hex: %w", err))
				continue
			}
			tags = append(tags, cpsTag{kind: "b", data: data})
		case "r", "rc", "rd":
			n, err := strconv.Atoi(val)
			if err != nil {
				errs = append(errs, fmt.Errorf("bad <%s> count: %w", key, err))
				continue
			}
			tags = append(tags, cpsTag{kind: key, n: n})
		default:
			errs = append(errs, fmt.Errorf("unknown tag <%s>", key))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return &testObfChain{tags: tags}, nil
}

// obfuscate renders the chain like obfChain.Obfuscate(dst, nil): static bytes
// verbatim, <r N> = N random bytes, <rc N> = N random letters, <rd N> = N random
// digits. The result is the full decoy datagram.
func (c *testObfChain) obfuscate() []byte {
	var out []byte
	for _, tag := range c.tags {
		switch tag.kind {
		case "b":
			out = append(out, tag.data...)
		case "r":
			buf := make([]byte, tag.n)
			rand.Read(buf)
			out = append(out, buf...)
		case "rc":
			buf := make([]byte, tag.n)
			rand.Read(buf)
			for i := range buf {
				buf[i] = testChars52[buf[i]%52]
			}
			out = append(out, buf...)
		case "rd":
			buf := make([]byte, tag.n)
			rand.Read(buf)
			for i := range buf {
				buf[i] = testDigits10[buf[i]%10]
			}
			out = append(out, buf...)
		}
	}
	return out
}
