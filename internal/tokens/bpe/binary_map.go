package bpe

const (
	bytesPerLevel = 6
	maxRank       = 0x7FFFFFFF
)

func byteAt(k []byte, index int) uint64 {
	if index < 0 || index >= len(k) {
		return 0
	}
	return uint64(k[index])
}

// BinaryMapKey computes the same 48-bit key shape used by the TypeScript BinaryMap.
func BinaryMapKey(k []byte, start, end int) uint64 {
	length := max(end-start, 0)

	lowerShift := 0
	if v := (3 - length) * 8; v > 0 {
		lowerShift = v
	}
	lowerMask := uint64(0xFFFFFF >> lowerShift)
	lower := (byteAt(k, start+0) | (byteAt(k, start+1) << 8) | (byteAt(k, start+2) << 16)) & lowerMask

	upperShift := min(max((6-length)*8, 0), 31)
	upperMask := uint64(0xFFFFFF >> upperShift)
	upper := (byteAt(k, start+3) | (byteAt(k, start+4) << 8) | (byteAt(k, start+5) << 16)) & upperMask

	return lower + (0x1000000 * upper)
}

type BinaryMap[V any] struct {
	nested map[uint64]*BinaryMap[V]
	final  map[uint64]V
}

func NewBinaryMap[V any]() *BinaryMap[V] {
	return &BinaryMap[V]{
		nested: map[uint64]*BinaryMap[V]{},
		final:  map[uint64]V{},
	}
}

func (b *BinaryMap[V]) Get(key []byte) (V, bool) {
	return b.GetRange(key, 0, len(key))
}

func (b *BinaryMap[V]) GetRange(key []byte, start, end int) (V, bool) {
	var zero V
	if start < 0 {
		start = 0
	}
	if end < start {
		return zero, false
	}

	isFinal := end < bytesPerLevel+start
	mapKey := BinaryMapKey(key, start, end)
	if isFinal {
		v, ok := b.final[mapKey]
		return v, ok
	}

	next, ok := b.nested[mapKey]
	if !ok {
		return zero, false
	}
	return next.GetRange(key, bytesPerLevel+start, end)
}

func (b *BinaryMap[V]) Set(key []byte, value V) {
	k := BinaryMapKey(key, 0, len(key))
	isFinal := len(key) < bytesPerLevel
	if isFinal {
		b.final[k] = value
		return
	}

	if next, ok := b.nested[k]; ok {
		next.Set(key[bytesPerLevel:], value)
		return
	}

	next := NewBinaryMap[V]()
	next.Set(key[bytesPerLevel:], value)
	b.nested[k] = next
}
