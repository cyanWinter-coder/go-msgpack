// Copyright (c) 2012-2018 Ugorji Nwoke. All rights reserved.
// Use of this source code is governed by a MIT license found in the LICENSE file.

package codec_test

import (
	"bytes"
	"math"
	"reflect"
	"testing"
	"time"

	codec "github.com/hashicorp/go-msgpack/v2/codec"
)

// UID420Holder has a schema-less interface field, the location where integer
// widths and named/registered representations are easiest to lose.
type UID420Holder struct {
	V any `codec:"v" json:"v"`
}

type UID420Ptr struct {
	Name string `codec:"name"`
}

type UID420Ext struct {
	S string
}

func (UID420Ext) WriteExt(v any) []byte {
	switch x := v.(type) {
	case UID420Ext:
		return []byte(x.S)
	case *UID420Ext:
		return []byte(x.S)
	default:
		return nil
	}
}

func (*UID420Ext) ReadExt(dst any, b []byte) {
	if x, ok := dst.(*UID420Ext); ok {
		x.S = string(b)
	}
}

func uid420Msgpack(t *testing.T, writeExt bool) *codec.MsgpackHandle {
	t.Helper()
	h := &codec.MsgpackHandle{WriteExt: writeExt}
	if err := h.SetBytesExt(reflect.TypeOf(UID420Ext{}), 42, &UID420Ext{}); err != nil {
		t.Fatalf("SetBytesExt: %v", err)
	}
	return h
}

func uid420Encode(t *testing.T, h codec.Handle, v any) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := codec.NewEncoder(&buf, h).Encode(v); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

func uid420Decode(t *testing.T, h codec.Handle, b []byte, dst any) error {
	t.Helper()
	return codec.NewDecoderBytes(append([]byte(nil), b...), h).Decode(dst)
}

func TestUID420IntegerBoundariesRoundTripIntoInterface(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		proto  any
		values []any
	}{
		{"int8", int8(0), []any{int8(math.MinInt8), int8(-1), int8(0), int8(math.MaxInt8)}},
		{"int16", int16(0), []any{int16(math.MinInt16), int16(-1), int16(0), int16(math.MaxInt16)}},
		{"int32", int32(0), []any{int32(math.MinInt32), int32(-1), int32(0), int32(math.MaxInt32)}},
		{"int64", int64(0), []any{int64(math.MinInt64), int64(-1), int64(0), int64(math.MaxInt64)}},
		{"uint8", uint8(0), []any{uint8(0), uint8(1), uint8(math.MaxUint8)}},
		{"uint16", uint16(0), []any{uint16(0), uint16(1), uint16(math.MaxUint16)}},
		{"uint32", uint32(0), []any{uint32(0), uint32(1), uint32(math.MaxUint32)}},
		{"uint64", uint64(0), []any{uint64(0), uint64(1), uint64(math.MaxUint64)}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &codec.MsgpackHandle{}
			for _, want := range tc.values {
				wire := uid420Encode(t, h, UID420Holder{V: want})
				got := UID420Holder{V: tc.proto}
				if err := uid420Decode(t, h, wire, &got); err != nil {
					t.Fatalf("decode %T(%#v): %v", want, want, err)
				}
				if !reflect.DeepEqual(got.V, want) {
					t.Fatalf("round trip changed: got %T(%#v), want %T(%#v); wire=% x",
						got.V, got.V, want, want, wire)
				}
			}
		})
	}
}

func TestUID420UnsafeIntegerConversionsFailAndPreserveTarget(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		proto any
		bad   any
	}{
		{"int8-overflow", int8(7), int64(math.MaxInt8 + 1)},
		{"int16-overflow", int16(7), int64(math.MaxInt16 + 1)},
		{"int32-overflow", int32(7), int64(math.MaxInt32 + 1)},
		{"int64-from-uint64-overflow", int64(7), uint64(math.MaxUint64)},
		{"uint8-negative", uint8(7), int8(-1)},
		{"uint16-negative", uint16(7), int16(-1)},
		{"uint32-negative", uint32(7), int32(-1)},
		{"uint64-negative", uint64(7), int64(-1)},
		{"uint8-overflow", uint8(7), uint16(math.MaxUint8 + 1)},
		{"uint16-overflow", uint16(7), uint32(math.MaxUint16 + 1)},
		{"uint32-overflow", uint32(7), uint64(math.MaxUint32 + 1)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &codec.MsgpackHandle{}
			wire := uid420Encode(t, h, UID420Holder{V: tc.bad})
			dst := UID420Holder{V: tc.proto}
			want := dst
			err := uid420Decode(t, h, wire, &dst)
			if err == nil {
				t.Fatalf("expected error decoding %T(%#v) into %T", tc.bad, tc.bad, tc.proto)
			}
			if !reflect.DeepEqual(dst, want) {
				t.Fatalf("failed decode modified target: got %#v, want %#v (err=%v)", dst, want, err)
			}
		})
	}
}

func TestUID420SignedIntegerOptionDoesNotWrapOverflow(t *testing.T) {
	t.Parallel()
	h := &codec.MsgpackHandle{}
	h.SignedInteger = true

	wire := uid420Encode(t, h, uint64(math.MaxUint64))
	var got any
	if err := uid420Decode(t, h, wire, &got); err != nil {
		t.Fatal(err)
	}
	u, ok := got.(uint64)
	if !ok || u != math.MaxUint64 {
		t.Fatalf("got %T(%#v), want lossless uint64", got, got)
	}

	wire = uid420Encode(t, h, uint64(math.MaxInt64))
	got = nil
	if err := uid420Decode(t, h, wire, &got); err != nil {
		t.Fatal(err)
	}
	i, ok := got.(int64)
	if !ok || i != math.MaxInt64 {
		t.Fatalf("got %T(%#v), want int64 MaxInt64", got, got)
	}
}

func TestUID420RegisteredExtTimeAndRaw(t *testing.T) {
	t.Parallel()

	h := uid420Msgpack(t, true)
	now := time.Date(2024, time.February, 3, 4, 5, 6, 7, time.UTC)

	t.Run("registered extension restores concrete type", func(t *testing.T) {
		want := UID420Holder{V: UID420Ext{S: "registered"}}
		wire := uid420Encode(t, h, want)

		var got UID420Holder
		if err := uid420Decode(t, h, wire, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.V, want.V) {
			t.Fatalf("got %T(%#v), want %T(%#v)", got.V, got.V, want.V, want.V)
		}
	})

	t.Run("time extension restores time.Time", func(t *testing.T) {
		want := UID420Holder{V: now}
		wire := uid420Encode(t, h, want)

		var got UID420Holder
		if err := uid420Decode(t, h, wire, &got); err != nil {
			t.Fatal(err)
		}
		gotTime, ok := got.V.(time.Time)
		if !ok {
			t.Fatalf("got %T, want time.Time", got.V)
		}
		if !gotTime.Equal(now) {
			t.Fatalf("time changed: got %v, want %v", gotTime, now)
		}
	})

	t.Run("legacy time wire decodes into existing time target", func(t *testing.T) {
		legacy := &codec.MsgpackHandle{WriteExt: false}
		wire := uid420Encode(t, legacy, UID420Holder{V: now})
		got := UID420Holder{V: time.Time{}}
		if err := uid420Decode(t, legacy, wire, &got); err != nil {
			t.Fatal(err)
		}
		gotTime, ok := got.V.(time.Time)
		if !ok || !gotTime.Equal(now) {
			t.Fatalf("got %T(%#v), want time.Time %v", got.V, got.V, now)
		}
	})

	t.Run("unknown extension is lossless RawExt", func(t *testing.T) {
		wantRE := codec.RawExt{Tag: 78, Data: []byte{0xde, 0xad, 0xbe, 0xef}}
		wire := uid420Encode(t, h, UID420Holder{V: wantRE})

		var got UID420Holder
		if err := uid420Decode(t, h, wire, &got); err != nil {
			t.Fatal(err)
		}
		re, ok := got.V.(codec.RawExt)
		if !ok {
			t.Fatalf("got %T, want codec.RawExt", got.V)
		}
		if re.Tag != wantRE.Tag || !bytes.Equal(re.Data, wantRE.Data) {
			t.Fatalf("RawExt changed: got %+v, want %+v", re, wantRE)
		}
		if againWire := uid420Encode(t, h, UID420Holder{V: got.V}); !bytes.Equal(againWire, wire) {
			t.Fatalf("RawExt is not lossless: re-encoded % x, want % x", againWire, wire)
		}
	})

	t.Run("Raw preserves complete wire value", func(t *testing.T) {
		wire := uid420Encode(t, h, "raw-payload")
		var raw codec.Raw
		if err := uid420Decode(t, h, wire, &raw); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal([]byte(raw), wire) {
			t.Fatalf("Raw = % x, want wire % x", []byte(raw), wire)
		}
	})
}

func TestUID420NilTypedNilAndEmptyContainers(t *testing.T) {
	t.Parallel()
	h := &codec.MsgpackHandle{}

	t.Run("untyped nil", func(t *testing.T) {
		got := UID420Holder{V: int8(1)}
		if err := uid420Decode(t, h, []byte{0xc0}, &got.V); err != nil {
			t.Fatal(err)
		}
		if got.V != nil {
			t.Fatalf("got %#v, want untyped nil", got.V)
		}
	})

	var nilPtr *UID420Ptr
	t.Run("typed nil pointer in struct field", func(t *testing.T) {
		got := UID420Holder{V: nilPtr}
		if err := uid420Decode(t, h, []byte{0x81, 0xa1, 'v', 0xc0}, &got); err != nil {
			t.Fatal(err)
		}
		if got.V == nil {
			t.Fatal("interface became untyped nil")
		}
		p, ok := got.V.(*UID420Ptr)
		if !ok {
			t.Fatalf("got %T, want *UID420Ptr", got.V)
		}
		if p != nil {
			t.Fatalf("pointer = %#v, want typed nil", p)
		}
	})

	t.Run("typed nil slice and empty slice are stable", func(t *testing.T) {
		gotNil := UID420Holder{V: []int(nil)}
		if err := uid420Decode(t, h, []byte{0x81, 0xa1, 'v', 0xc0}, &gotNil); err != nil {
			t.Fatal(err)
		}
		s, ok := gotNil.V.([]int)
		if !ok || s != nil {
			t.Fatalf("typed nil slice got %T(%#v)", gotNil.V, gotNil.V)
		}

		gotEmpty := UID420Holder{V: []int(nil)}
		if err := uid420Decode(t, h, []byte{0x81, 0xa1, 'v', 0x90}, &gotEmpty); err != nil {
			t.Fatal(err)
		}
		s, ok = gotEmpty.V.([]int)
		if !ok || s == nil || len(s) != 0 {
			t.Fatalf("empty slice got %T(%#v)", gotEmpty.V, gotEmpty.V)
		}

		wire := uid420Encode(t, h, gotEmpty)
		var again UID420Holder
		if err := uid420Decode(t, h, wire, &again); err != nil {
			t.Fatal(err)
		}
		if rv := reflect.ValueOf(again.V); rv.Kind() != reflect.Slice || rv.IsNil() || rv.Len() != 0 {
			t.Fatalf("empty slice not stable: %T(%#v)", again.V, again.V)
		}
	})

	t.Run("typed nil map and empty map are stable", func(t *testing.T) {
		gotNil := UID420Holder{V: map[string]int(nil)}
		if err := uid420Decode(t, h, []byte{0x81, 0xa1, 'v', 0xc0}, &gotNil); err != nil {
			t.Fatal(err)
		}
		m, ok := gotNil.V.(map[string]int)
		if !ok || m != nil {
			t.Fatalf("typed nil map got %T(%#v)", gotNil.V, gotNil.V)
		}

		gotEmpty := UID420Holder{V: map[string]int(nil)}
		if err := uid420Decode(t, h, []byte{0x81, 0xa1, 'v', 0x80}, &gotEmpty); err != nil {
			t.Fatal(err)
		}
		m, ok = gotEmpty.V.(map[string]int)
		if !ok || m == nil || len(m) != 0 {
			t.Fatalf("empty map got %T(%#v)", gotEmpty.V, gotEmpty.V)
		}
	})

	t.Run("fresh interface empty containers are non-nil", func(t *testing.T) {
		var slice any
		if err := uid420Decode(t, h, []byte{0x90}, &slice); err != nil {
			t.Fatal(err)
		}
		if rv := reflect.ValueOf(slice); rv.Kind() != reflect.Slice || rv.IsNil() || rv.Len() != 0 {
			t.Fatalf("fresh empty slice = %T(%#v)", slice, slice)
		}

		var m any
		if err := uid420Decode(t, h, []byte{0x80}, &m); err != nil {
			t.Fatal(err)
		}
		if rv := reflect.ValueOf(m); rv.Kind() != reflect.Map || rv.IsNil() || rv.Len() != 0 {
			t.Fatalf("fresh empty map = %T(%#v)", m, m)
		}
	})
}

type UID420Partial struct {
	A int    `codec:"a" json:"a"`
	B string `codec:"b" json:"b"`
}

func TestUID420FailedDecodeLeavesExistingTargetUnchanged(t *testing.T) {
	t.Parallel()

	// Msgpack fixmap with a valid first field and an integer where B's string
	// decoder expects a length/descriptor.
	msgpackBad := []byte{0x82, 0xa1, 'a', 0x02, 0xa1, 'b', 0x2a}
	for name, h := range map[string]codec.Handle{
		"msgpack": &codec.MsgpackHandle{},
		"json":    &codec.JsonHandle{},
	} {
		t.Run(name, func(t *testing.T) {
			wire := msgpackBad
			if name == "json" {
				wire = []byte(`{"a":2 "b":"x"}`)
			}
			dst := UID420Partial{A: 1, B: "old"}
			want := dst
			err := uid420Decode(t, h, wire, &dst)
			if err == nil {
				t.Fatal("expected type error")
			}
			if !reflect.DeepEqual(dst, want) {
				t.Fatalf("target changed after failed decode: got %#v, want %#v (err=%v)", dst, want, err)
			}
		})
	}
}

func uid420NestedValue() UID420Holder {
	var nilPtr *UID420Ptr
	return UID420Holder{V: map[string]any{
		"i8":  int8(-8),
		"u16": uint16(160),
		"u64": uint64(math.MaxUint64),
		"t":   time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
		"ext": UID420Ext{S: "nested"},
		"slice": []any{
			int8(1),
			[]any{uint32(math.MaxUint32), "deep"},
		},
		"nil-in-slice": []any{nilPtr},
		"typed-nil":    nilPtr,
	}}
}

func TestUID420NestedInterfacesPreserveTypes(t *testing.T) {
	t.Parallel()
	h := uid420Msgpack(t, true)

	wire := uid420Encode(t, h, uid420NestedValue())
	dst := uid420NestedValue()
	want := uid420NestedValue()
	if err := uid420Decode(t, h, wire, &dst); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(dst, want) {
		t.Fatalf("nested round trip mismatch\ngot:  %#v\nwant: %#v", dst, want)
	}
}
