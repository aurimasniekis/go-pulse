package banagent

import (
	"math/big"
	"net/netip"
)

// prefixBits recovers the prefix length of an interval from its matching end (the smallest end above the start).
// Intervals pulsed did not write (not a power-of-two size) fall back to a single host.
func prefixBits(startKey []byte, ends []*big.Int) int {
	total := len(startKey) * 8
	start := new(big.Int).SetBytes(startKey)
	var best *big.Int
	for _, e := range ends {
		if e.Cmp(start) > 0 && (best == nil || e.Cmp(best) < 0) {
			best = e
		}
	}
	if best == nil {
		return total
	}
	size := new(big.Int).Sub(best, start)
	if size.BitLen() == 0 || new(big.Int).And(size, new(big.Int).Sub(size, big.NewInt(1))).Sign() != 0 {
		return total
	}
	return total - (size.BitLen() - 1)
}

// end returns the first address after a prefix (the interval end key).
func end(p netip.Prefix) []byte {
	a := p.Addr().AsSlice()
	n := new(big.Int).SetBytes(a)
	n.Add(n, new(big.Int).Lsh(big.NewInt(1), uint(len(a)*8-p.Bits()))) //nolint:gosec // bits ≤ 128
	b := n.Bytes()
	out := make([]byte, len(a))
	if len(b) > len(a) { // wrapped past the end of the address space
		return out
	}
	copy(out[len(a)-len(b):], b)
	return out
}
