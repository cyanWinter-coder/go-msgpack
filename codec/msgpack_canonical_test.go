// Copyright (c) 2012-2018 Ugorji Nwoke. All rights reserved.
// Use of this source code is governed by a MIT license found in the LICENSE file.

package codec

import (
	"bytes"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

// testMsgpackCanonicalHandle returns a handle with canonical (deterministic)
// encoding enabled, independent of the global flag-mutated test handles.
func testMsgpackCanonicalHandle() (h *MsgpackHandle) {
	h = &MsgpackHandle{}
	h.WriteExt = true
	h.Canonical = true
	return
}

// testMsgpackEncodeErr encodes v with h, returning the bytes and error.
func testMsgpackEncodeErr(v interface{}, h *MsgpackHandle) (bs []byte, err error) {
	err = NewEncoderBytes(&bs, h).Encode(v)
	return
}

// TestMsgpackCanonicalDeterministic verifies that the same logical value
// always encodes to the same sequence of bytes, irrespective of map
// insertion order or how many times it is encoded.
func TestMsgpackCanonicalDeterministic(t *testing.T) {
	testOnce.Do(testInitAll)
	h := testMsgpackCanonicalHandle()

	newValue := func() map[string]interface{} {
		return map[string]interface{}{
			"z": []interface{}{3, "a", true, nil},
			"a": map[string]interface{}{
				"k2": "v2",
				"k1": map[int]string{3: "c", 1: "a", 2: "b"},
			},
			"m": map[int]interface{}{
				-1: "neg",
				5:  []uint{1, 2, 3},
				22: map[bool]int{true: 1, false: 0},
			},
		}
	}

	// encode the same logical value many times; all outputs must be identical
	var golden []byte
	for i := 0; i < 32; i++ {
		bs, err := testMsgpackEncodeErr(newValue(), h)
		if err != nil {
			t.Fatalf("encode failed: %v", err)
		}
		if golden == nil {
			golden = bs
		} else if !bytes.Equal(golden, bs) {
			t.Fatalf("run %d: unequal bytes:\n%x\nvs\n%x", i, bs, golden)
		}
	}

	// build maps with shuffled insertion order; output must still be identical
	r := rand.New(rand.NewSource(42))
	keys := []string{"k1", "k2", "k3", "k4", "k5", "k6", "k7", "k8"}
	for i := 0; i < 32; i++ {
		perm := r.Perm(len(keys))
		m := make(map[string]int, len(keys))
		for _, j := range perm {
			m[keys[j]] = j
		}
		bs, err := testMsgpackEncodeErr(m, h)
		if err != nil {
			t.Fatalf("encode failed: %v", err)
		}
		// decode and re-encode: bytes must be stable across a round-trip
		var m2 map[string]int
		if err = NewDecoderBytes(bs, h).Decode(&m2); err != nil {
			t.Fatalf("decode failed: %v", err)
		}
		bs2, err := testMsgpackEncodeErr(m2, h)
		if err != nil {
			t.Fatalf("re-encode failed: %v", err)
		}
		if !bytes.Equal(bs, bs2) {
			t.Fatalf("round-trip not stable:\n%x\nvs\n%x", bs, bs2)
		}
	}
}

// TestMsgpackCanonicalMapKeyOrder verifies that map keys are written in a
// stable, well-defined order: natural order for bool/int/uint/float/string/
// time.Time keys, and canonical encoded bytes for all other key types.
func TestMsgpackCanonicalMapKeyOrder(t *testing.T) {
	testOnce.Do(testInitAll)
	h := testMsgpackCanonicalHandle()

	type structKey struct {
		A string
	}

	var table = []struct {
		v      interface{}
		expect string // hex
	}{
		// string keys: lexicographic order
		{map[string]int{"b": 1, "a": 2}, "82a16102a16201"},
		// int keys: numeric order (negative first)
		{map[int]string{5: "e", -1: "n", 200: "x"}, "83ffa16e05a165d100c8a178"},
		// uint keys: numeric order
		{map[uint8]int{2: 2, 1: 1, 255: 3}, "8301010202ccff03"},
		// bool keys: false before true
		{map[bool]int{true: 1, false: 0}, "82c200c301"},
		// float keys: numeric order
		{map[float64]int{1.5: 3, -2.5: 1, 0: 2}, "83cbc00400000000000001cb000000000000000002cb3ff800000000000003"},
		// time.Time keys: chronological order
		{map[time.Time]int{
			time.Unix(100, 0).UTC(): 1,
			time.Unix(-5, 0).UTC():  2,
		}, "82c70cff00000000fffffffffffffffb02d6ff0000006401"},
		// struct keys: sorted by canonical encoded bytes
		{map[structKey]int{{"y"}: 2, {"x"}: 1}, "8281a141a1780181a141a17902"},
		// interface keys: sorted by canonical encoded bytes
		// (0x01 for int 1, 0xa1 'a' for "a", 0xc3 for true)
		{map[interface{}]int{true: 3, "a": 2, 1: 1}, "830101a16102c303"},
	}

	for i, tt := range table {
		bs, err := testMsgpackEncodeErr(tt.v, h)
		if err != nil {
			t.Fatalf("entry %d: encode failed: %v", i, err)
		}
		if got := bytesToHex(bs); got != tt.expect {
			t.Fatalf("entry %d (%T): got %s, expected %s", i, tt.v, got, tt.expect)
		}
		// the canonical stream must decode back to an equal value
		rt := reflect.TypeOf(tt.v)
		rv := reflect.New(rt)
		if err = NewDecoderBytes(bs, h).Decode(rv.Interface()); err != nil {
			t.Fatalf("entry %d: decode failed: %v", i, err)
		}
	}
}

// TestMsgpackTimeExtBoundaries verifies that the time extension keeps its
// type tag (-1) and precision at the boundaries of the timestamp32,
// timestamp64 and timestamp96 formats.
func TestMsgpackTimeExtBoundaries(t *testing.T) {
	testOnce.Do(testInitAll)
	h := &MsgpackHandle{}
	h.WriteExt = true

	var table = []struct {
		tm     time.Time
		expect string // hex; empty means just check round-trip
	}{
		// zero time encodes as nil
		{time.Time{}, "c0"},
		// timestamp32: nsec == 0 and seconds fit in 32 bits
		{time.Unix(0, 0).UTC(), "d6ff00000000"},
		{time.Unix(1<<32-1, 0).UTC(), "d6ffffffffff"},
		// timestamp64: seconds fit in 34 bits
		{time.Unix(1<<32, 0).UTC(), "d7ff0000000100000000"},
		{time.Unix(0, 1).UTC(), "d7ff0000000400000000"},
		{time.Unix(1<<34-1, 999999999).UTC(), "d7ffee6b27ffffffffff"},
		// timestamp96: everything else (negative or large seconds)
		{time.Unix(-1, 0).UTC(), "c70cff00000000ffffffffffffffff"},
		{time.Unix(1<<34, 0).UTC(), "c70cff000000000000000400000000"},
		{time.Unix(-62135596800, 999999999).UTC(), "c70cff3b9ac9fffffffff1886e0900"},
	}

	for i, tt := range table {
		bs, err := testMsgpackEncodeErr(tt.tm, h)
		if err != nil {
			t.Fatalf("entry %d (%v): encode failed: %v", i, tt.tm, err)
		}
		if tt.expect != "" {
			if got := bytesToHex(bs); got != tt.expect {
				t.Fatalf("entry %d (%v): got %s, expected %s", i, tt.tm, got, tt.expect)
			}
		}
		if tt.tm.IsZero() {
			continue
		}
		// the extension tag must be -1 (0xff)
		if bs[0] == 0xd6 || bs[0] == 0xd7 {
			if bs[1] != 0xff {
				t.Fatalf("entry %d (%v): ext tag is 0x%x, expected 0xff (-1)", i, tt.tm, bs[1])
			}
		} else if bs[0] == 0xc7 {
			if bs[2] != 0xff {
				t.Fatalf("entry %d (%v): ext tag is 0x%x, expected 0xff (-1)", i, tt.tm, bs[2])
			}
		} else {
			t.Fatalf("entry %d (%v): unexpected descriptor 0x%x", i, tt.tm, bs[0])
		}
		// round-trip must preserve the instant exactly
		var tm2 time.Time
		if err = NewDecoderBytes(bs, h).Decode(&tm2); err != nil {
			t.Fatalf("entry %d (%v): decode failed: %v", i, tt.tm, err)
		}
		if !tm2.Equal(tt.tm) {
			t.Fatalf("entry %d: times not equal: %v vs %v", i, tm2, tt.tm)
		}
		if tm2.Nanosecond() != tt.tm.Nanosecond() {
			t.Fatalf("entry %d: nanoseconds not preserved: %d vs %d", i, tm2.Nanosecond(), tt.tm.Nanosecond())
		}
	}
}

// TestMsgpackTimeOutOfRange verifies that a time which cannot be represented
// returns a clear error, and does not leave a partial value in the output.
func TestMsgpackTimeOutOfRange(t *testing.T) {
	testOnce.Do(testInitAll)
	h := &MsgpackHandle{}
	h.WriteExt = true
	h.TimeNotBuiltin = true

	// a zone offset beyond what time.Time.MarshalBinary supports
	// (its offset in minutes must fit in an int16)
	tm := time.Date(2020, 1, 1, 0, 0, 0, 0, time.FixedZone("BIG", 30*24*60*60))
	if _, err := tm.MarshalBinary(); err == nil {
		t.Fatalf("precondition failed: expected MarshalBinary to reject %v", tm)
	}

	bs, err := testMsgpackEncodeErr(tm, h)
	if err == nil {
		t.Fatalf("expected error encoding out-of-range time, got none (bytes: %x)", bs)
	}
	if len(bs) != 0 {
		t.Fatalf("partial value written on error: %x", bs)
	}

	// same, nested inside a map value: the whole encode must fail atomically
	bs, err = testMsgpackEncodeErr(map[string]time.Time{"k": tm}, h)
	if err == nil {
		t.Fatalf("expected error encoding out-of-range time in map, got none")
	}
	if len(bs) != 0 {
		t.Fatalf("partial value written on error: %x", bs)
	}
}

// TestMsgpackEncodeErrorNoPartialOutput verifies that a failed encode
// (circular reference, unencodable key or value) never leaves a partially
// written value in the output buffer.
func TestMsgpackEncodeErrorNoPartialOutput(t *testing.T) {
	testOnce.Do(testInitAll)

	type circ struct {
		S string
		T *circ
	}
	c := &circ{S: "x"}
	c.T = c

	hCirc := testMsgpackCanonicalHandle()
	hCirc.CheckCircularRef = true

	h := testMsgpackCanonicalHandle()

	var table = []struct {
		name string
		h    *MsgpackHandle
		v    interface{}
	}{
		{"circular-ref", hCirc, c},
		{"unencodable-key", h, map[complex128]int{complex(1, 2): 1}},
		{"unencodable-value", h, map[string]interface{}{"a": complex(1, 2)}},
		{"unencodable-nested", h, map[string]interface{}{"a": []interface{}{1, complex(2, 3)}}},
	}

	for _, tt := range table {
		bs, err := testMsgpackEncodeErr(tt.v, tt.h)
		if err == nil {
			t.Fatalf("%s: expected error, got none (bytes: %x)", tt.name, bs)
		}
		if len(bs) != 0 {
			t.Fatalf("%s: partial value written on error: %x", tt.name, bs)
		}
	}

	// a failed encode must not corrupt values already written to the buffer:
	// encoding a good value followed by a bad one keeps the good value intact.
	var buf []byte
	enc := NewEncoderBytes(&buf, h)
	if err := enc.Encode(map[string]int{"a": 1}); err != nil {
		t.Fatalf("first encode failed: %v", err)
	}
	goodLen := len(buf)
	if err := enc.Encode(map[string]interface{}{"b": complex(1, 2)}); err == nil {
		t.Fatalf("expected error on second encode")
	}
	if len(buf) != goodLen {
		t.Fatalf("buffer changed by failed encode: was %d bytes, now %d", goodLen, len(buf))
	}
	var m map[string]int
	if err := NewDecoderBytes(buf, h).Decode(&m); err != nil || m["a"] != 1 {
		t.Fatalf("remaining buffer does not hold first value: %v, %v", m, err)
	}

	// MustEncode must also roll back before panicking
	buf = nil
	func() {
		defer func() {
			if recover() == nil {
				t.Fatalf("expected panic from MustEncode")
			}
		}()
		NewEncoderBytes(&buf, h).MustEncode(map[string]interface{}{"b": complex(1, 2)})
	}()
	if len(buf) != 0 {
		t.Fatalf("MustEncode left partial value: %x", buf)
	}
}

// TestMsgpackCanonicalCompatible verifies that enabling Canonical does not
// change the encoding of non-map values, and that canonical map encodings
// decode correctly with a non-canonical handle.
func TestMsgpackCanonicalCompatible(t *testing.T) {
	testOnce.Do(testInitAll)
	hCanon := testMsgpackCanonicalHandle()
	hPlain := &MsgpackHandle{}
	hPlain.WriteExt = true

	// non-map values encode identically with and without Canonical
	values := []interface{}{
		"hello",
		int64(-33),
		uint64(1 << 40),
		true,
		[]int{1, 2, 3},
		struct {
			A string
			B int
		}{"x", 2},
		time.Unix(1234567, 890).UTC(),
	}
	for i, v := range values {
		b1, err := testMsgpackEncodeErr(v, hCanon)
		if err != nil {
			t.Fatalf("entry %d: canonical encode failed: %v", i, err)
		}
		b2, err := testMsgpackEncodeErr(v, hPlain)
		if err != nil {
			t.Fatalf("entry %d: plain encode failed: %v", i, err)
		}
		if !bytes.Equal(b1, b2) {
			t.Fatalf("entry %d (%T): canonical %x != plain %x", i, v, b1, b2)
		}
	}

	// a canonical map stream decodes fine with a plain (non-canonical) handle
	v := map[string]interface{}{"b": 1, "a": "x"}
	bs, err := testMsgpackEncodeErr(v, hCanon)
	if err != nil {
		t.Fatalf("canonical encode failed: %v", err)
	}
	var m map[string]interface{}
	if err = NewDecoderBytes(bs, hPlain).Decode(&m); err != nil {
		t.Fatalf("plain decode of canonical stream failed: %v", err)
	}
	if len(m) != 2 || m["a"] != "x" || fmt.Sprintf("%v", m["b"]) != "1" {
		t.Fatalf("decoded map mismatch: %#v", m)
	}
}

// bytesToHex returns the hex representation of bs, for golden comparisons.
func bytesToHex(bs []byte) string {
	const hexdig = "0123456789abcdef"
	var sb strings.Builder
	for _, b := range bs {
		sb.WriteByte(hexdig[b>>4])
		sb.WriteByte(hexdig[b&0xf])
	}
	return sb.String()
}

// BenchmarkMsgpackCanonicalMapEncode profiles canonical vs regular map
// encoding (run with -bench=. and -benchmem, and analyze with pprof).
func BenchmarkMsgpackCanonicalMapEncode(b *testing.B) {
	m := make(map[string]interface{}, 64)
	for i := 0; i < 64; i++ {
		m[string(rune('a'+i%26))+string(rune('a'+i/26))] = map[int]string{
			i:     "v",
			i + 1: "w",
		}
	}
	hPlain := &MsgpackHandle{}
	hPlain.WriteExt = true
	hCanon := testMsgpackCanonicalHandle()

	b.Run("regular", func(b *testing.B) {
		var bs []byte
		enc := NewEncoderBytes(&bs, hPlain)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			bs = bs[:0]
			enc.ResetBytes(&bs)
			if err := enc.Encode(m); err != nil {
				b.Fatalf("encode failed: %v", err)
			}
		}
	})
	b.Run("canonical", func(b *testing.B) {
		var bs []byte
		enc := NewEncoderBytes(&bs, hCanon)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			bs = bs[:0]
			enc.ResetBytes(&bs)
			if err := enc.Encode(m); err != nil {
				b.Fatalf("encode failed: %v", err)
			}
		}
	})
}
