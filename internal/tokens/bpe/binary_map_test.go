package bpe

import "testing"

func TestBinaryMapBasicOneLevel(t *testing.T) {
	binMap := NewBinaryMap[int]()
	binMap.Set([]byte{1, 50, 24}, 1)

	if v, ok := binMap.Get([]byte{1, 50, 24}); !ok || v != 1 {
		t.Fatalf("expected key to map to 1, got %v (ok=%v)", v, ok)
	}
	if _, ok := binMap.Get([]byte{1, 50}); ok {
		t.Fatalf("expected short key to miss")
	}
	if _, ok := binMap.Get([]byte{1, 50, 24, 100}); ok {
		t.Fatalf("expected long key to miss before insert")
	}

	binMap.Set([]byte{1, 50, 24, 100}, 100)
	if v, ok := binMap.Get([]byte{1, 50, 24, 100}); !ok || v != 100 {
		t.Fatalf("expected key to map to 100, got %v (ok=%v)", v, ok)
	}
}

func TestBinaryMapBasicOneOrTwoLevels(t *testing.T) {
	binMap := NewBinaryMap[int]()
	binMap.Set([]byte{1, 50, 24, 34, 64, 23}, 1)
	binMap.Set([]byte{1, 50, 24, 34, 64, 23, 60, 120, 40}, 2)
	binMap.Set([]byte{1, 50, 24, 34, 64, 23, 60, 120, 40, 21, 54, 232}, 3)

	check := func(key []byte, want int) {
		t.Helper()
		got, ok := binMap.Get(key)
		if !ok || got != want {
			t.Fatalf("expected %v => %d, got %d (ok=%v)", key, want, got, ok)
		}
	}

	check([]byte{1, 50, 24, 34, 64, 23}, 1)
	check([]byte{1, 50, 24, 34, 64, 23, 60, 120, 40}, 2)
	check([]byte{1, 50, 24, 34, 64, 23, 60, 120, 40, 21, 54, 232}, 3)
}

func TestBinaryMapGetWithRange(t *testing.T) {
	binMap := NewBinaryMap[int]()
	binMap.Set([]byte{64, 23}, 100)
	binMap.Set([]byte{1, 50, 24}, 1)
	binMap.Set([]byte{24, 34, 64}, 2)
	binMap.Set([]byte{23, 60, 120, 1, 50, 24}, 255)

	mainArray := []byte{64, 23, 60, 120, 1, 50, 24, 34, 64}
	assertRange := func(start, end, want int, shouldExist bool) {
		t.Helper()
		got, ok := binMap.GetRange(mainArray, start, end)
		if ok != shouldExist {
			t.Fatalf("range (%d,%d) expected ok=%v got %v", start, end, shouldExist, ok)
		}
		if shouldExist && got != want {
			t.Fatalf("range (%d,%d) expected %d got %d", start, end, want, got)
		}
	}

	assertRange(4, 7, 1, true)
	assertRange(6, 9, 2, true)
	assertRange(1, 7, 255, true)
	assertRange(7, 7, 0, false)
	assertRange(6, 10, 2, true)
	assertRange(0, 2, 100, true)
}

func TestBinaryMapKey(t *testing.T) {
	tests := []struct {
		name       string
		arr        []byte
		start, end int
		want       uint64
	}{
		{name: "first 3 max bytes", arr: []byte{0xFF, 0xFF, 0xFF, 0xAB, 0xCD, 0xEF}, start: 0, end: 6, want: 0xEFCDABFFFFFF},
		{name: "all 6 max bytes", arr: []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, start: 0, end: 6, want: 0xFFFFFFFFFFFF},
		{name: "first 3 min bytes", arr: []byte{0x00, 0x00, 0x00, 0xAB, 0xCD, 0xEF}, start: 0, end: 6, want: 0xEFCDAB000000},
		{name: "last 3 min bytes", arr: []byte{0xAB, 0xCD, 0xEF, 0x00, 0x00, 0x00}, start: 0, end: 6, want: 0x000000EFCDAB},
		{name: "assorted bytes", arr: []byte{0xBA, 0xDC, 0xFE, 0xEF, 0xCD, 0xAB}, start: 0, end: 6, want: 0xABCDEFFEDCBA},
		{name: "lower bits range", arr: []byte{0xBA, 0xDC, 0xFE, 0xEF, 0xCD, 0xAB}, start: 1, end: 3, want: 0x00000000FEDC},
		{name: "upper bits range", arr: []byte{0xBA, 0xDC, 0xFE, 0xEF, 0xCD, 0xAB}, start: 3, end: 6, want: 0x000000ABCDEF},
		{name: "cross range", arr: []byte{0xBA, 0xDC, 0xFE, 0xEF, 0xCD, 0xAB}, start: 2, end: 5, want: 0x000000CDEFFE},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BinaryMapKey(tt.arr, tt.start, tt.end)
			if got != tt.want {
				t.Fatalf("expected %X got %X", tt.want, got)
			}
		})
	}
}
