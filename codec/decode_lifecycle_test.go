// Copyright (c) 2012-2018 Ugorji Nwoke. All rights reserved.
// Use of this source code is governed by a MIT license found in the LICENSE file.

package codec_test

import (
	"bytes"
	"io"
	"reflect"
	"testing"

	codec "github.com/hashicorp/go-msgpack/v2/codec"
)

// newLifecycleHandles returns handles exercising every reader path:
// direct bytes, plain io.Reader and buffered io.Reader.
func newLifecycleHandles() map[string]codec.Handle {
	plain := &codec.MsgpackHandle{}
	bufio := &codec.MsgpackHandle{}
	bufio.ReaderBufferSize = 64
	return map[string]codec.Handle{
		"bytes": plain,
		"io":    plain,
		"bufio": bufio,
	}
}

func lifecycleDecoder(name string, stream []byte, h codec.Handle) *codec.Decoder {
	if name == "bytes" {
		return codec.NewDecoderBytes(append([]byte(nil), stream...), h)
	}
	return codec.NewDecoder(bytes.NewReader(append([]byte(nil), stream...)), h)
}

// TestDecoderOneValuePerDecode verifies that a Decode call consumes at most
// one top-level value, and that the remaining bytes are read by the next call,
// regardless of whether the input is a byte slice or an io.Reader.
func TestDecoderOneValuePerDecode(t *testing.T) {
	// three msgpack positive fixnums: 1, 2, 3
	stream := []byte{0x01, 0x02, 0x03}
	for name, h := range newLifecycleHandles() {
		d := lifecycleDecoder(name, stream, h)
		for i, want := range []int{1, 2, 3} {
			var got int
			if err := d.Decode(&got); err != nil {
				t.Fatalf("%s: frame %d: unexpected error: %v", name, i, err)
			}
			if got != want {
				t.Fatalf("%s: frame %d: got %d, want %d", name, i, got, want)
			}
		}
		if d.NumBytesRead() != len(stream) {
			t.Fatalf("%s: read %d bytes, want %d", name, d.NumBytesRead(), len(stream))
		}
		// the stream is now exhausted; the next Decode must report EOF
		var extra int
		if err := d.Decode(&extra); err != io.EOF {
			t.Fatalf("%s: expected io.EOF after final frame, got %v", name, err)
		}
	}
}

// TestDecoderDefaultDetachBytes verifies that values delivered by default do
// not alias the caller's reusable input buffer, across every way a []byte can
// reach the caller.
func TestDecoderDefaultDetachBytes(t *testing.T) {
	// bin8, length 3, "abc"
	frame := []byte{0xc4, 0x03, 'a', 'b', 'c'}
	h := &codec.MsgpackHandle{}

	t.Run("typed []byte", func(t *testing.T) {
		buf := append([]byte(nil), frame...)
		d := codec.NewDecoderBytes(buf, h)
		var got []byte
		if err := d.Decode(&got); err != nil {
			t.Fatal(err)
		}
		for i := range buf {
			buf[i] = 'X'
		}
		if string(got) != "abc" {
			t.Fatalf("decoded bytes alias input buffer: got %q, want abc", got)
		}
	})

	t.Run("interface{} []byte", func(t *testing.T) {
		buf := append([]byte(nil), frame...)
		d := codec.NewDecoderBytes(buf, h)
		var v interface{}
		if err := d.Decode(&v); err != nil {
			t.Fatal(err)
		}
		for i := range buf {
			buf[i] = 'X'
		}
		got, ok := v.([]byte)
		if !ok {
			t.Fatalf("got %T, want []byte", v)
		}
		if string(got) != "abc" {
			t.Fatalf("naked bytes alias input buffer: got %q, want abc", got)
		}
	})

	t.Run("BinaryUnmarshaler", func(t *testing.T) {
		buf := append([]byte(nil), frame...)
		d := codec.NewDecoderBytes(buf, h)
		var v lifecycleRetainBin
		if err := d.Decode(&v); err != nil {
			t.Fatal(err)
		}
		for i := range buf {
			buf[i] = 'X'
		}
		if string(v.data) != "abc" {
			t.Fatalf("UnmarshalBinary received aliased bytes: got %q, want abc", v.data)
		}
	})

	t.Run("RawExt", func(t *testing.T) {
		// fixext2, tag 5, payload "hi"
		extFrame := []byte{0xd5, 0x05, 'h', 'i'}
		buf := append([]byte(nil), extFrame...)
		d := codec.NewDecoderBytes(buf, h)
		var re codec.RawExt
		if err := d.Decode(&re); err != nil {
			t.Fatal(err)
		}
		for i := range buf {
			buf[i] = 'X'
		}
		if string(re.Data) != "hi" {
			t.Fatalf("RawExt.Data aliases input buffer: got %q, want hi", re.Data)
		}
	})
}

// lifecycleBinType implements both Binary(M|Unm)arshaler so the decoder
// routes through (*Decoder).binaryUnmarshal.
type lifecycleBinType []byte

func (l lifecycleBinType) MarshalBinary() ([]byte, error) { return []byte(l), nil }
func (l *lifecycleBinType) UnmarshalBinary(b []byte) error {
	*l = append((*l)[:0], b...)
	return nil
}

// lifecycleRetainBin keeps the exact slice it is given, so tests can observe
// whether it aliases the decoder's input buffer.
type lifecycleRetainBin struct{ data []byte }

func (l lifecycleRetainBin) MarshalBinary() ([]byte, error) { return l.data, nil }
func (l *lifecycleRetainBin) UnmarshalBinary(b []byte) error {
	l.data = b
	return nil
}

// TestDecoderZeroCopyMode documents and enforces the explicit opt-in: with
// ZeroCopy the decoder may return a view into the input, transferring
// ownership/validity responsibility to the caller.
func TestDecoderZeroCopyMode(t *testing.T) {
	frame := []byte{0xc4, 0x03, 'a', 'b', 'c'}

	// the BinaryUnmarshaler path requests bytes with zerocopy=true internally.
	t.Run("view allowed when enabled", func(t *testing.T) {
		h := &codec.MsgpackHandle{}
		h.ZeroCopy = true
		buf := append([]byte(nil), frame...)
		d := codec.NewDecoderBytes(buf, h)
		var v lifecycleRetainBin
		if err := d.Decode(&v); err != nil {
			t.Fatal(err)
		}
		if len(v.data) != 3 {
			t.Fatalf("got len %d, want 3", len(v.data))
		}
		// In zero-copy mode the view is permitted to alias the input:
		// mutating the input payload is observed through the decoded value.
		buf[4] = 'X' // last payload byte ('c')
		if v.data[2] != 'X' {
			t.Fatalf("ZeroCopy did not return a view into the input: got %q", v.data)
		}
	})

	t.Run("copy enforced by default", func(t *testing.T) {
		h := &codec.MsgpackHandle{}
		buf := append([]byte(nil), frame...)
		d := codec.NewDecoderBytes(buf, h)
		var v lifecycleRetainBin
		if err := d.Decode(&v); err != nil {
			t.Fatal(err)
		}
		buf[4] = 'X'
		if string(v.data) != "abc" {
			t.Fatalf("default mode leaked a view into the input: got %q", v.data)
		}
	})
}

// TestDecoderStickyErrorAndReset verifies that truncation, illegal container
// lengths and type errors put the stream in a sticky error state, while Reset
// / ResetBytes clear every counter and error so the Decoder can be reused.
func TestDecoderStickyErrorAndReset(t *testing.T) {
	cases := map[string][]byte{
		"truncated bytes":   {0xc4, 0x0a, 'a', 'b', 'c'}, // bin8 claims 10, gives 3
		"bad descriptor":    {0xc1},                      // never-used msgpack byte
		"truncated array":   {0x92, 0x01},                // array of 2 with 1 elem
		"illegal map len":   {0xdf, 0x10, 0xee, 0xee, 0xee, 0x1, 0xa1, 0x1},
		"truncated nesting": {0x92, 0x92, 0x01}, // nested array cut short
	}
	for name, b := range cases {
		for _, reader := range []string{"bytes", "io"} {
			bh := &codec.MsgpackHandle{}
			d := lifecycleDecoder(reader, b, bh)
			var v interface{}
			first := d.Decode(&v)
			if first == nil {
				t.Fatalf("%s/%s: expected an error decoding malformed frame", name, reader)
			}
			// every subsequent call must return the same sticky error
			var n int
			if second := d.Decode(&n); second == nil {
				t.Fatalf("%s/%s: decoder did not stay in error state after %q",
					name, reader, first)
			}

			// Reset must fully revive the decoder
			if reader == "bytes" {
				d.ResetBytes([]byte{0x07})
			} else {
				d.Reset(bytes.NewReader([]byte{0x07}))
			}
			var z int
			if err := d.Decode(&z); err != nil {
				t.Fatalf("%s/%s: decoder unusable after Reset: %v", name, reader, err)
			}
			if z != 7 {
				t.Fatalf("%s/%s: got %d after Reset, want 7", name, reader, z)
			}
			if d.NumBytesRead() != 1 {
				t.Fatalf("%s/%s: byte counter not reset: %d", name, reader, d.NumBytesRead())
			}
		}
	}

	// a type error (string in the stream decoded into an int) is sticky too
	for _, reader := range []string{"bytes", "io"} {
		d := lifecycleDecoder(reader, []byte{0xa3, 'a', 'b', 'c'}, &codec.MsgpackHandle{})
		var n int
		if err := d.Decode(&n); err == nil {
			t.Fatalf("type-mismatch/%s: expected an error", reader)
		}
		if err := d.Decode(&n); err == nil {
			t.Fatalf("type-mismatch/%s: expected the error to remain sticky", reader)
		}
	}
}

// TestDecoderResetClearsDepth verifies that the nesting counter does not
// survive a Reset, even when the previous frame failed mid-container.
func TestDecoderResetClearsDepth(t *testing.T) {
	h := &codec.MsgpackHandle{}
	d := codec.NewDecoderBytes([]byte{0x92, 0x92, 0x92, 0x01}, h)
	var v interface{}
	if err := d.Decode(&v); err == nil {
		t.Fatal("expected an error from truncated nested containers")
	}
	// after Reset, a deeply nested but complete value must decode; a stale
	// depth would make this trip MaxDepth spuriously.
	d.ResetBytes(encodeNestedArrays(t, h, 10))
	var got interface{}
	if err := d.Decode(&got); err != nil {
		t.Fatalf("depth not cleared by Reset: %v", err)
	}
}

func encodeNestedArrays(t *testing.T, h codec.Handle, depth int) []byte {
	// build [[...[1]...]] with `depth` array wrappers via the encoder
	var buf bytes.Buffer
	e := codec.NewEncoder(&buf, h)
	var encode func(remaining int, val interface{}) error
	encode = func(remaining int, val interface{}) error {
		if remaining == 0 {
			return e.Encode(val)
		}
		return encode(remaining-1, []interface{}{val})
	}
	if err := encode(depth, 1); err != nil {
		t.Fatalf("setup encode failed: %v", err)
	}
	return buf.Bytes()
}

// TestDecoderBytesAndReaderParity verifies that byte inputs and io.Reader
// inputs (plain and buffered) produce equivalent decoded values and identical
// error positions.
func TestDecoderBytesAndReaderParity(t *testing.T) {
	cases := map[string][]byte{
		"truncated bytes":   {0xc4, 0x0a, 'a', 'b', 'c'},
		"bad descriptor":    {0xc1},
		"truncated array":   {0x92, 0x01},
		"illegal map len":   {0xdf, 0x10, 0xee, 0xee, 0xee, 0x1, 0xa1, 0x1},
		"nested truncation": {0x92, 0x92, 0x01},
		// bin32 claiming 100 bytes but only 70 follow; exceeds the 64-byte
		// buffer below, exercising the large-read fill path.
		"large truncation": append(append([]byte{0xc6, 0, 0, 0, 100}, make([]byte, 70)...), 1),
	}

	// newParityDecoder builds a decoder per input mode.
	newParityDecoder := func(mode string, b []byte) *codec.Decoder {
		switch mode {
		case "bytes":
			return codec.NewDecoderBytes(append([]byte(nil), b...), &codec.MsgpackHandle{})
		case "io":
			return codec.NewDecoder(bytes.NewReader(append([]byte(nil), b...)), &codec.MsgpackHandle{})
		default: // bufio
			h := &codec.MsgpackHandle{}
			h.ReaderBufferSize = 64
			return codec.NewDecoder(bytes.NewReader(append([]byte(nil), b...)), h)
		}
	}

	for name, b := range cases {
		ref := newParityDecoder("bytes", b)
		var refVal interface{}
		refErr := ref.Decode(&refVal)
		refPos := ref.NumBytesRead()

		for _, mode := range []string{"io", "bufio"} {
			d := newParityDecoder(mode, b)
			var val interface{}
			err := d.Decode(&val)
			pos := d.NumBytesRead()

			if (refErr == nil) != (err == nil) {
				t.Fatalf("%s/%s: error mismatch: bytes=%v %s=%v", name, mode, refErr, mode, err)
			}
			if pos != refPos {
				t.Fatalf("%s/%s: position mismatch: bytes=%d %s=%d", name, mode, refPos, mode, pos)
			}
			if refErr == nil && !reflect.DeepEqual(refVal, val) {
				t.Fatalf("%s/%s: value mismatch: bytes=%v %s=%v", name, mode, refVal, mode, val)
			}
		}
	}

	// successful streams must decode identically too, including binary fields
	good := []byte{0x93, 0x01, 0x02, 0xc4, 0x02, 'h', 'i'}
	var vb, vr interface{}
	if err := codec.NewDecoderBytes(append([]byte(nil), good...), &codec.MsgpackHandle{}).Decode(&vb); err != nil {
		t.Fatal(err)
	}
	if err := codec.NewDecoder(bytes.NewReader(append([]byte(nil), good...)), &codec.MsgpackHandle{}).Decode(&vr); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(vb, vr) {
		t.Fatalf("good stream mismatch: bytes=%v reader=%v", vb, vr)
	}
}
