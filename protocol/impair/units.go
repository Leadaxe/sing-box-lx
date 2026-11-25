package impair

import (
	"strconv"
	"strings"
	"time"

	E "github.com/sagernet/sing/common/exceptions"
)

// Value parsers. Every parser takes the key path for its error text, so a
// bad value names exactly where it is ("impair slow-se: up.rate: ...").

const (
	minRate = 8_000          // 8kbit
	maxRate = 10_000_000_000 // 10gbit
)

// parseRate parses "10mbit", "1.5mbit", "500kbit", "8000bit", "1gbit" into
// bits per second. "unlimited" is 0 (no rate limit). Units are decimal
// (1 mbit = 1 000 000 bit/s); byte-based spellings (kbps, mbps, MB/s) are
// refused because tools disagree on whether they mean bits or bytes.
func parseRate(key, s string) (float64, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	if v == "unlimited" {
		return 0, nil
	}
	units := []struct {
		suffix string
		mult   float64
	}{{"gbit", 1e9}, {"mbit", 1e6}, {"kbit", 1e3}, {"bit", 1}}
	for _, unit := range units {
		if strings.HasSuffix(v, unit.suffix) {
			number, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(v, unit.suffix)), 64)
			if err != nil || number <= 0 {
				break
			}
			rate := number * unit.mult
			if rate < minRate || rate > maxRate {
				return 0, E.New(key, ": rate ", strconv.Quote(s), " must be within 8kbit..10gbit or \"unlimited\"")
			}
			return rate, nil
		}
	}
	return 0, E.New(key, ": invalid rate ", strconv.Quote(s), `: want a number with bit, kbit, mbit or gbit ("10mbit"), or "unlimited"`)
}

// parseDuration parses a Go duration ("20ms", "1.5s") within [min, max].
func parseDuration(key, s string, min, max time.Duration) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return 0, E.New(key, ": invalid duration ", strconv.Quote(s), ` (want e.g. "20ms", "1.5s")`)
	}
	if d < min || d > max {
		return 0, E.New(key, ": ", strconv.Quote(s), " must be within ", min, "..", max)
	}
	return d, nil
}

// parsePercent parses "0.5%" into 0.005, within 0..100 %.
func parsePercent(key, s string) (float64, error) {
	v := strings.TrimSpace(s)
	if !strings.HasSuffix(v, "%") {
		return 0, E.New(key, ": ", strconv.Quote(s), ` must be a percentage ("0.5%")`)
	}
	number, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(v, "%")), 64)
	if err != nil || number < 0 || number > 100 {
		return 0, E.New(key, ": ", strconv.Quote(s), " must be a percentage within 0%..100%")
	}
	return number / 100, nil
}

const (
	minQueueBytes = 1500
	maxQueueBytes = 64 << 20
	minQueueTime  = time.Millisecond
	maxQueueTime  = 10 * time.Second
)

// parseQueue parses the bottleneck queue: a duration of transmission at the
// current rate ("50ms") or a byte size ("64KB", "1MB", "30000B"; KB and MB
// are 1024-based).
func parseQueue(key, s string) (queueTime time.Duration, queueBytes int, err error) {
	v := strings.TrimSpace(s)
	if d, durationErr := time.ParseDuration(v); durationErr == nil {
		if d < minQueueTime || d > maxQueueTime {
			return 0, 0, E.New(key, ": queue ", strconv.Quote(s), " must be within 1ms..10s")
		}
		return d, 0, nil
	}
	upper := strings.ToUpper(v)
	units := []struct {
		suffix string
		mult   float64
	}{{"MB", 1 << 20}, {"KB", 1 << 10}, {"B", 1}}
	for _, unit := range units {
		if strings.HasSuffix(upper, unit.suffix) {
			number, parseErr := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(upper, unit.suffix)), 64)
			if parseErr != nil {
				break
			}
			bytes := int(number * unit.mult)
			if bytes < minQueueBytes || bytes > maxQueueBytes {
				return 0, 0, E.New(key, ": queue ", strconv.Quote(s), " must be within 1500B..64MB")
			}
			return 0, bytes, nil
		}
	}
	return 0, 0, E.New(key, ": invalid queue ", strconv.Quote(s), ` (want a duration at the rate, "50ms", or a size, "64KB")`)
}
