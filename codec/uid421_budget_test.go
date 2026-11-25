// Copyright (c) 2012-2018 Ugorji Nwoke. All rights reserved.
// Use of this source code is governed by a MIT license found in the LICENSE file.

package codec

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"testing"
)

func uid421BudgetTestDecoder(t *testing.T, mode string, in []byte, h *MsgpackHandle) *Decoder {
	t.Helper()
	b := append([]byte(nil), in...)
	switch mode {
	case "bytes":
		return NewDecoderBytes(b, h)
	case "reader":
		return NewDecoder(bytes.NewReader(b), h)
	case "hidden-reader":
		return NewDecoder(ioReaderWrapper{bytes.NewReader(b)}, h)
	case "bufio":
		h.ReaderBufferSize = 64
		return NewDecoder(bytes.NewReader(b), h)
	default:
		t.Fatalf("unknown decoder mode %q", mode)
		return nil
	}
}

func assertUID421BudgetDecoderRevives(t *testing.T, d *Decoder, mode string) {
	t.Helper()
	d.ResetBytes([]byte{0x07})
	var n int
	if err := d.Decode(&n); err != nil {
		t.Fatalf("%s: Reset did not restore decoder budget: %v", mode, err)
	}
	if n != 7 {
		t.Fatalf("%s: after Reset got %d, want 7", mode, n)
	}
}

func TestDecodeUID421BudgetDeclaredLengthAndShortRead(t *testing.T) {
	// These headers all announce more entries than the remaining bytes can
	// possibly contain. The largest declarations are especially important:
	// a regression that allocates before checking would try to make a huge
	// map/slice backing store.
	payloads := map[string][]byte{
		"array32-max":   {0xdd, 0xff, 0xff, 0xff, 0xff},
		"map32-max":     {0xdf, 0xff, 0xff, 0xff, 0xff},
		"array16-short": {0xdc, 0x00, 0x02, 0x01},
		"map16-short":   {0xde, 0x00, 0x01},
		"bin8-short":    {0xc4, 0x0a, 'a', 'b', 'c'},
		"nested-short":  {0x92, 0x92, 0x01},
	}

	for name, in := range payloads {
		for _, mode := range []string{"bytes", "reader", "bufio", "hidden-reader"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				h := &MsgpackHandle{}
				d := uid421BudgetTestDecoder(t, mode, in, h)

				var v interface{}
				err := d.Decode(&v)
				if err == nil {
					t.Fatal("expected declared-length payload to fail")
				}
				if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) &&
					!errors.Is(err, ErrContainerLengthExceeded) {
					t.Fatalf("expected short-read or length-limit error, got %v", err)
				}

				var n int
				if err2 := d.Decode(&n); err2 == nil {
					t.Fatal("decoder accepted input after a budget failure")
				} else if !errors.Is(err2, io.EOF) && !errors.Is(err2, io.ErrUnexpectedEOF) &&
					!errors.Is(err2, ErrContainerLengthExceeded) {
					t.Fatalf("sticky error changed to %v", err2)
				}

				assertUID421BudgetDecoderRevives(t, d, mode)
			})
		}
	}
}

func TestDecodeUID421BudgetConfiguredContainerLimit(t *testing.T) {
	cases := []struct {
		name   string
		in     []byte
		decode func(*Decoder) error
	}{
		{
			name: "interface-array",
			in:   []byte{0x93, 0x01, 0x02, 0x03},
			decode: func(d *Decoder) error {
				var v interface{}
				return d.Decode(&v)
			},
		},
		{
			name: "typed-slice",
			in:   []byte{0x93, 0x01, 0x02, 0x03},
			decode: func(d *Decoder) error {
				var v []int
				return d.Decode(&v)
			},
		},
		{
			name: "interface-map",
			in:   []byte{0x82, 0xa1, 'a', 0x01, 0xa1, 'b', 0x02},
			decode: func(d *Decoder) error {
				var v interface{}
				return d.Decode(&v)
			},
		},
		{
			name: "typed-map",
			in:   []byte{0x82, 0xa1, 'a', 0x01, 0xa1, 'b', 0x02},
			decode: func(d *Decoder) error {
				var v map[string]int
				return d.Decode(&v)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &MsgpackHandle{}
			h.MaxContainerLen = 1
			d := NewDecoderBytes(tc.in, h)

			err := tc.decode(d)
			if !errors.Is(err, ErrContainerLengthExceeded) {
				t.Fatalf("expected ErrContainerLengthExceeded, got %v", err)
			}

			if err := tc.decode(d); !errors.Is(err, ErrContainerLengthExceeded) {
				t.Fatalf("expected sticky container uid421Budget error, got %v", err)
			}

			// A one-entry container remains valid under the same limit.
			d.ResetBytes([]byte{0x91, 0x01})
			var one []int
			if err := d.Decode(&one); err != nil {
				t.Fatalf("valid one-entry array rejected after Reset: %v", err)
			}
			if len(one) != 1 || one[0] != 1 {
				t.Fatalf("got %v, want [1]", one)
			}
		})
	}
}

func uid421BudgetNestedArrays(depth int) []byte {
	b := []byte{0x01}
	for i := 0; i < depth; i++ {
		b = append([]byte{0x91}, b...)
	}
	return b
}

func uid421BudgetNestedMaps(depth int) []byte {
	b := []byte{0x01}
	for i := 0; i < depth; i++ {
		b = append([]byte{0x81, 0xa1, 'k'}, b...)
	}
	return b
}

func uid421BudgetNestedMixed(depth int) []byte {
	b := []byte{0x01}
	for i := depth; i > 0; i-- {
		if i%2 == 0 {
			b = append([]byte{0x91}, b...)
		} else {
			b = append([]byte{0x81, 0xa1, 'k'}, b...)
		}
	}
	return b
}

func TestDecodeUID421BudgetSharedDepthAndReset(t *testing.T) {
	payloads := map[string][]byte{
		"arrays": uid421BudgetNestedArrays(64),
		"maps":   uid421BudgetNestedMaps(64),
		"mixed":  uid421BudgetNestedMixed(64),
	}

	for name, in := range payloads {
		for _, mode := range []string{"bytes", "reader", "bufio"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				h := &MsgpackHandle{}
				h.MaxDepth = 4
				d := uid421BudgetTestDecoder(t, mode, in, h)

				var v interface{}
				err := d.Decode(&v)
				if !errors.Is(err, ErrMaxDepthExceeded) {
					t.Fatalf("expected ErrMaxDepthExceeded, got %v", err)
				}

				var n int
				if err := d.Decode(&n); !errors.Is(err, ErrMaxDepthExceeded) {
					t.Fatalf("depth error was not sticky: %v", err)
				}

				// Reset must clear the half-unwound depth counter. A shallow
				// complete value then succeeds under the same configured limit.
				d.ResetBytes(uid421BudgetNestedArrays(2))
				var shallow interface{}
				if err := d.Decode(&shallow); err != nil {
					t.Fatalf("Reset did not clear depth budget: %v", err)
				}
			})
		}
	}
}

func TestDecodeUID421BudgetPriorValueRemainsCompleteAfterFailure(t *testing.T) {
	stream := append([]byte{0x01}, []byte{0xdf, 0xff, 0xff, 0xff, 0xff}...)
	d := NewDecoderBytes(stream, &MsgpackHandle{})

	var first int
	if err := d.Decode(&first); err != nil {
		t.Fatalf("first complete value failed: %v", err)
	}
	if first != 1 {
		t.Fatalf("first value = %d, want 1", first)
	}

	var bad interface{}
	if err := d.Decode(&bad); err == nil {
		t.Fatal("expected oversized map header to fail")
	}

	var third int
	if err := d.Decode(&third); err == nil {
		t.Fatal("decoder was reused after budget failure")
	}
}

func TestUID421RawOwnedBufferAndTrackingRelease(t *testing.T) {
	// fixstr length 5: Raw must contain the complete encoded value, including
	// its descriptor byte.
	want := []byte{0xa5, 'h', 'e', 'l', 'l', 'o'}

	for _, mode := range []string{"bytes", "reader", "bufio"} {
		t.Run(mode, func(t *testing.T) {
			h := &MsgpackHandle{}
			src := append([]byte(nil), want...)
			var d *Decoder
			if mode == "bytes" {
				// Deliberately pass the reusable buffer directly so mutating
				// src after Decode detects an aliased Raw result.
				d = NewDecoderBytes(src, h)
			} else {
				d = uid421BudgetTestDecoder(t, mode, want, h)
			}

			var raw Raw
			if err := d.Decode(&raw); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, want) {
				t.Fatalf("Raw = %x, want %x", raw, want)
			}

			for i := range src {
				src[i] = 'X'
			}
			if !bytes.Equal(raw, want) {
				t.Fatalf("Raw aliased reusable input: %x", raw)
			}

			// The io tracking buffer is transferred to Raw and detached from
			// the reader. There must not be a second reader-owned reference.
			switch mode {
			case "reader":
				if d.r.ri.tr != nil {
					t.Fatal("io reader retained Raw tracking buffer")
				}
			case "bufio":
				if d.r.bi.tr != nil {
					t.Fatal("buffered reader retained Raw tracking buffer")
				}
			}
		})
	}
}

func TestUID421BudgetZeroSizeContainersUseAllocationCap(t *testing.T) {
	// The reader wrapper hides bytes.Reader.Len, forcing the decoder to rely
	// on its allocation cap rather than the remaining-input preflight.
	in := []byte{0xdf, 0xff, 0xff, 0xff, 0xff}
	h := &MsgpackHandle{}
	d := uid421BudgetTestDecoder(t, "hidden-reader", in, h)

	var m map[struct{}]struct{}
	if err := d.Decode(&m); err == nil {
		t.Fatal("expected oversized zero-size map to fail")
	}

	var s []struct{}
	d.Reset(ioReaderWrapper{bytes.NewReader([]byte{0xdd, 0xff, 0xff, 0xff, 0xff})})
	if err := d.Decode(&s); err == nil {
		t.Fatal("expected oversized zero-size slice to fail")
	}
}

func TestUID421BudgetInferLenHonorsConfiguredCapForZeroSizeUnits(t *testing.T) {
	if got := decInferLen(1<<30, 0, 0); got != 4096 {
		t.Fatalf("zero-size unit default cap: got %d, want 4096", got)
	}
	if got := decInferLen(1<<30, 16, 0); got != 16 {
		t.Fatalf("zero-size unit with explicit cap: got %d, want 16", got)
	}
	if got := decInferLen(32, 16, 0); got != 16 {
		t.Fatalf("zero-size unit above cap: got %d, want 16", got)
	}
	if got := decInferLen(8, 16, 0); got != 8 {
		t.Fatalf("zero-size unit below cap: got %d, want 8", got)
	}
}

var errUID421BudgetTestConversion = errors.New("uid421 budget test conversion failed")

type uid421BudgetTempExtType struct {
	Data []byte
}

type uid421BudgetFailExt struct{}

func (uid421BudgetFailExt) WriteExt(interface{}) []byte { panic("unexpected WriteExt") }
func (uid421BudgetFailExt) ReadExt(_ interface{}, _ []byte) {
	panic(errUID421BudgetTestConversion)
}

func TestDecodeUID421BudgetFailedConversionReleasesTempReference(t *testing.T) {
	h := &MsgpackHandle{}
	if err := h.SetBytesExt(reflect.TypeOf(uid421BudgetTempExtType{}), 77, uid421BudgetFailExt{}); err != nil {
		t.Fatal(err)
	}

	// ext8, length 3, tag 77, payload "abc".
	in := []byte{0xc7, 0x03, 77, 'a', 'b', 'c'}
	d := NewDecoderBytes(in, h)

	var v interface{}
	err := d.Decode(&v)
	if !errors.Is(err, errUID421BudgetTestConversion) {
		t.Fatalf("expected conversion error, got %v", err)
	}
	if d.n.l != nil {
		t.Fatal("decoder retained temporary naked byte buffer after failed conversion")
	}

	var n int
	if err := d.Decode(&n); !errors.Is(err, errUID421BudgetTestConversion) {
		t.Fatalf("conversion failure was not sticky: %v", err)
	}

	d.ResetBytes([]byte{0x07})
	if err := d.Decode(&n); err != nil {
		t.Fatalf("Reset failed after conversion error: %v", err)
	}
	if n != 7 {
		t.Fatalf("n = %d, want 7", n)
	}
}
