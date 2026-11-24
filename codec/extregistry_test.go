// Copyright (c) 2012-2020 Ugorji Nwoke. All rights reserved.
// Use of this source code is governed by a MIT license found in the LICENSE file.

package codec

import (
	"bytes"
	"encoding/binary"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

// hotExtInt is the type used across the extension-registry tests.
type hotExtInt int64

var hotExtIntTyp = reflect.TypeOf(hotExtInt(0))

// hotExtA encodes hotExtInt big-endian; hotExtB encodes it little-endian,
// so the payload of each is recognizable on the wire.
type hotExtA struct{ onWrite func() }
type hotExtB struct{}

func (x hotExtA) WriteExt(v interface{}) (bs []byte) {
	if x.onWrite != nil {
		x.onWrite()
	}
	bs = make([]byte, 8)
	binary.BigEndian.PutUint64(bs, uint64(v.(hotExtInt)))
	return
}

func (x hotExtA) ReadExt(v interface{}, bs []byte) {
	*(v.(*hotExtInt)) = hotExtInt(binary.BigEndian.Uint64(bs))
}

func (x hotExtB) WriteExt(v interface{}) (bs []byte) {
	bs = make([]byte, 8)
	binary.LittleEndian.PutUint64(bs, uint64(v.(hotExtInt)))
	return
}

func (x hotExtB) ReadExt(v interface{}, bs []byte) {
	*(v.(*hotExtInt)) = hotExtInt(binary.LittleEndian.Uint64(bs))
}

func testExtRegEncode(t *testing.T, h *MsgpackHandle, v interface{}) (bs []byte) {
	t.Helper()
	if err := NewEncoderBytes(&bs, h).Encode(v); err != nil {
		t.Fatalf("encode %T: %v", v, err)
	}
	return
}

func testExtRegDecode(t *testing.T, h *MsgpackHandle, bs []byte, v interface{}) {
	t.Helper()
	if err := NewDecoderBytes(bs, h).Decode(v); err != nil {
		t.Fatalf("decode into %T: %v", v, err)
	}
}

// testExtRegTag extracts the msgpack extension tag from a top-level
// fixext8/ext8 encoding of a single value.
func testExtRegTag(t *testing.T, bs []byte) byte {
	t.Helper()
	switch bs[0] {
	case 0xd4, 0xd5, 0xd6, 0xd7, 0xd8: // fixext 1/2/4/8/16
		return bs[1]
	case 0xc7: // ext 8
		return bs[2]
	default:
		t.Fatalf("expected ext descriptor, got 0x%x", bs[0])
		return 0
	}
}

func TestExtRegistryGenerations(t *testing.T) {
	reg := NewExtRegistry()
	if g := reg.Generation(); g != 0 {
		t.Fatalf("new registry: generation = %d, want 0", g)
	}

	s0 := reg.Snapshot()
	if s0.Len() != 0 {
		t.Fatalf("empty snapshot: len = %d, want 0", s0.Len())
	}

	if err := reg.RegisterBytesExt(hotExtIntTyp, 11, hotExtA{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if g := reg.Generation(); g != 1 {
		t.Fatalf("after register: generation = %d, want 1", g)
	}

	s1 := reg.Snapshot()
	if s1.Len() != 1 {
		t.Fatalf("snapshot: len = %d, want 1", s1.Len())
	}
	ext, tag, ok := s1.Lookup(hotExtIntTyp)
	if !ok || tag != 11 || ext == nil {
		t.Fatalf("lookup: (%v, %d, %v), want (ext, 11, true)", ext, tag, ok)
	}
	ext, rt, ok := s1.LookupTag(11)
	if !ok || rt != hotExtIntTyp || ext == nil {
		t.Fatalf("lookup tag: (%v, %v, %v), want (ext, hotExtInt, true)", ext, rt, ok)
	}
	// lookup via the pointer type must resolve too (mirrors extHandle.getExt)
	if _, _, ok = s1.Lookup(reflect.TypeOf((*hotExtInt)(nil))); !ok {
		t.Fatalf("lookup via pointer type: not found")
	}

	// Re-registering the same type replaces the registration in a new
	// generation; the previously pinned snapshot is unaffected.
	if err := reg.RegisterBytesExt(hotExtIntTyp, 12, hotExtB{}); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	s2 := reg.Snapshot()
	if _, tag, _ = s2.Lookup(hotExtIntTyp); tag != 12 {
		t.Fatalf("new snapshot: tag = %d, want 12", tag)
	}
	if _, tag, _ = s1.Lookup(hotExtIntTyp); tag != 11 {
		t.Fatalf("old snapshot after replace: tag = %d, want 11", tag)
	}
	if _, _, ok = s2.LookupTag(11); ok {
		t.Fatalf("new snapshot: old tag 11 still resolvable")
	}

	// Unregister affects only later snapshots.
	reg.Unregister(hotExtIntTyp)
	s3 := reg.Snapshot()
	if _, _, ok = s3.Lookup(hotExtIntTyp); ok {
		t.Fatalf("snapshot after unregister: type still registered")
	}
	if _, _, ok = s2.Lookup(hotExtIntTyp); !ok {
		t.Fatalf("pre-unregister snapshot lost its registration")
	}

	// Release is idempotent, non-blocking, and only affects the snapshot.
	s2.Release()
	s2.Release()
	if _, _, ok = s2.Lookup(hotExtIntTyp); ok {
		t.Fatalf("released snapshot still resolves lookups")
	}
	if s2.Len() != 0 {
		t.Fatalf("released snapshot: len = %d, want 0", s2.Len())
	}
	if _, _, ok = s1.Lookup(hotExtIntTyp); !ok {
		t.Fatalf("release of one snapshot disturbed another")
	}
}

func TestExtRegistryRegisterValidation(t *testing.T) {
	reg := NewExtRegistry()
	if err := reg.RegisterBytesExt(hotExtIntTyp, 0x100, hotExtA{}); err == nil {
		t.Fatalf("expected error for tag > 0xff")
	}
	if err := reg.Register(reflect.TypeOf(time.Time{}), 1, nil); err == nil {
		t.Fatalf("expected error for natively supported type")
	}
	if err := reg.Register(reflect.TypeOf([]byte{}), 1, nil); err == nil {
		t.Fatalf("expected error for unnamed type")
	}
	if err := reg.Register(hotExtIntTyp, 1, nil); err == nil {
		t.Fatalf("expected error for nil ext")
	}
	if g := reg.Generation(); g != 0 {
		t.Fatalf("failed registrations bumped generation to %d", g)
	}
}

func TestMsgpackExtRegistryRoundTrip(t *testing.T) {
	reg := NewExtRegistry()
	h := &MsgpackHandle{WriteExt: true, ExtRegistry: reg}
	if err := reg.RegisterBytesExt(hotExtIntTyp, 11, hotExtA{}); err != nil {
		t.Fatal(err)
	}

	// typed round trip
	bs := testExtRegEncode(t, h, hotExtInt(42))
	if tag := testExtRegTag(t, bs); tag != 11 {
		t.Fatalf("encoded tag = %d, want 11", tag)
	}
	var hv hotExtInt
	testExtRegDecode(t, h, bs, &hv)
	if hv != 42 {
		t.Fatalf("round trip: got %d, want 42", hv)
	}

	// naked (interface{}) round trip resolves the registered type
	var v interface{}
	testExtRegDecode(t, h, bs, &v)
	if hv, ok := v.(hotExtInt); !ok || hv != 42 {
		t.Fatalf("naked round trip: got (%T) %v, want hotExtInt(42)", v, v)
	}

	// hot-swap: re-register with a new tag and ext; new calls use it
	if err := reg.RegisterBytesExt(hotExtIntTyp, 12, hotExtB{}); err != nil {
		t.Fatal(err)
	}
	bs = testExtRegEncode(t, h, hotExtInt(42))
	if tag := testExtRegTag(t, bs); tag != 12 {
		t.Fatalf("encoded tag after swap = %d, want 12", tag)
	}
	testExtRegDecode(t, h, bs, &hv)
	if hv != 42 {
		t.Fatalf("round trip after swap: got %d, want 42", hv)
	}

	// unregister: encoding falls back to the default (struct-less) encoding,
	// and decoding old ext bytes into interface{} degrades to RawExt
	reg.Unregister(hotExtIntTyp)
	bsOld := testExtRegEncode(t, h, hotExtInt(7)) // no ext: plain int now
	var n int64
	testExtRegDecode(t, h, bsOld, &n)
	if n != 7 {
		t.Fatalf("post-unregister round trip: got %d, want 7", n)
	}
}

func TestMsgpackExtRegistryActiveCallKeepsSnapshot(t *testing.T) {
	reg := NewExtRegistry()
	h := &MsgpackHandle{WriteExt: true, ExtRegistry: reg}

	// The first encode swaps the registry mid-call (from within WriteExt).
	// The in-flight call must still complete with the old generation (tag 11);
	// only the next call may observe the new generation (tag 12).
	var swapped bool
	extA := hotExtA{onWrite: func() {
		if !swapped {
			swapped = true
			if err := reg.RegisterBytesExt(hotExtIntTyp, 12, hotExtB{}); err != nil {
				t.Errorf("mid-call re-register: %v", err)
			}
		}
	}}
	if err := reg.RegisterBytesExt(hotExtIntTyp, 11, extA); err != nil {
		t.Fatal(err)
	}

	bs1 := testExtRegEncode(t, h, hotExtInt(1))
	if !swapped {
		t.Fatalf("mid-call swap did not happen")
	}
	if tag := testExtRegTag(t, bs1); tag != 11 {
		t.Fatalf("in-flight encode used tag %d, want pinned tag 11", tag)
	}

	bs2 := testExtRegEncode(t, h, hotExtInt(1))
	if tag := testExtRegTag(t, bs2); tag != 12 {
		t.Fatalf("post-swap encode used tag %d, want 12", tag)
	}

	// Decoding the old blob now hits an unknown tag (11 was replaced):
	// default policy degrades to RawExt.
	var v interface{}
	testExtRegDecode(t, h, bs1, &v)
	re, ok := v.(RawExt)
	if !ok {
		t.Fatalf("decode of stale-tag blob: got (%T) %v, want RawExt", v, v)
	}
	if re.Tag != 11 || !bytes.Equal(re.Data, bs1[2:]) {
		t.Fatalf("RawExt = %+v, want tag 11 with payload %x", re, bs1[2:])
	}
}

func TestMsgpackUnknownExtPolicy(t *testing.T) {
	reg := NewExtRegistry()
	h := &MsgpackHandle{WriteExt: true, ExtRegistry: reg}
	if err := reg.RegisterBytesExt(hotExtIntTyp, 21, hotExtA{}); err != nil {
		t.Fatal(err)
	}
	bs := testExtRegEncode(t, h, hotExtInt(9))
	reg.Unregister(hotExtIntTyp)

	// default policy: degrade to RawExt
	var v interface{}
	testExtRegDecode(t, h, bs, &v)
	if re, ok := v.(RawExt); !ok || re.Tag != 21 {
		t.Fatalf("default policy: got (%T) %+v, want RawExt{Tag:21}", v, v)
	}

	// error policy: recognizable error
	h.UnknownExtPolicy = UnknownExtError
	v = nil
	err := NewDecoderBytes(bs, h).Decode(&v)
	if err == nil {
		t.Fatalf("error policy: expected error, got value (%T) %v", v, v)
	}
	if !errors.Is(err, ErrUnknownExt) {
		t.Fatalf("error policy: errors.Is(err, ErrUnknownExt) = false for %v", err)
	}

	// the policy also applies without a registry (static handle extensions):
	// encode with a handle that has the ext, decode with one that does not
	h2 := &MsgpackHandle{WriteExt: true}
	if err := h2.SetBytesExt(hotExtIntTyp, 22, hotExtA{}); err != nil {
		t.Fatal(err)
	}
	bs2 := testExtRegEncode(t, h2, hotExtInt(9))
	h3 := &MsgpackHandle{WriteExt: true, UnknownExtPolicy: UnknownExtError}
	v = nil
	if err = NewDecoderBytes(bs2, h3).Decode(&v); !errors.Is(err, ErrUnknownExt) {
		t.Fatalf("static handle: errors.Is(err, ErrUnknownExt) = false for %v", err)
	}
}

func TestMsgpackExtRegistryStaticFallbackAndPrecedence(t *testing.T) {
	reg := NewExtRegistry()
	h := &MsgpackHandle{WriteExt: true, ExtRegistry: reg}

	// static registration on the handle, dynamic one in the registry
	if err := h.SetBytesExt(wrapBytesTyp, 32, &wrapBytesExt{}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterBytesExt(hotExtIntTyp, 11, hotExtA{}); err != nil {
		t.Fatal(err)
	}

	// static ext still applies for types not in the registry snapshot
	bs := testExtRegEncode(t, h, wrapBytes("hello"))
	if tag := testExtRegTag(t, bs); tag != 32 {
		t.Fatalf("static ext: encoded tag = %d, want 32", tag)
	}
	var wb wrapBytes
	testExtRegDecode(t, h, bs, &wb)
	if string(wb) != "hello" {
		t.Fatalf("static ext round trip: got %q", wb)
	}

	// registry registration for the same type takes precedence
	if err := reg.RegisterBytesExt(wrapBytesTyp, 33, &wrapBytesExt{}); err != nil {
		t.Fatal(err)
	}
	bs = testExtRegEncode(t, h, wrapBytes("hello"))
	if tag := testExtRegTag(t, bs); tag != 33 {
		t.Fatalf("registry over static: encoded tag = %d, want 33", tag)
	}

	// builtin types are unaffected by an attached registry
	now := time.Unix(1700000000, 12345).UTC()
	bs = testExtRegEncode(t, h, now)
	var tm time.Time
	testExtRegDecode(t, h, bs, &tm)
	if !tm.Equal(now) {
		t.Fatalf("time round trip: got %v, want %v", tm, now)
	}
}

func TestMsgpackExtRegistryGeneratedCodeCompat(t *testing.T) {
	reg := NewExtRegistry()
	h := &MsgpackHandle{WriteExt: true, ExtRegistry: reg}

	// codecgen types (Selfer) keep working with a registry attached
	in := wrapUint64(12345)
	bs := testExtRegEncode(t, h, in)
	var out wrapUint64
	testExtRegDecode(t, h, bs, &out)
	if out != in {
		t.Fatalf("codecgen round trip: got %d, want %d", out, in)
	}

	// a registry registration takes precedence over the generated code
	if err := reg.RegisterBytesExt(reflect.TypeOf(wrapUint64(0)), 41, wrapUint64Ext{}); err != nil {
		t.Fatal(err)
	}
	bs = testExtRegEncode(t, h, in)
	if tag := testExtRegTag(t, bs); tag != 41 {
		t.Fatalf("registry over codecgen: encoded tag = %d, want 41", tag)
	}
	testExtRegDecode(t, h, bs, &out)
	if out != in {
		t.Fatalf("registry-over-codecgen round trip: got %d, want %d", out, in)
	}
}

type wrapUint64Ext struct{}

func (wrapUint64Ext) WriteExt(v interface{}) (bs []byte) {
	bs = make([]byte, 8)
	binary.BigEndian.PutUint64(bs, uint64(v.(wrapUint64)))
	return
}

func (wrapUint64Ext) ReadExt(v interface{}, bs []byte) {
	*(v.(*wrapUint64)) = wrapUint64(binary.BigEndian.Uint64(bs))
}

func TestMsgpackExtRegistryConcurrent(t *testing.T) {
	reg := NewExtRegistry()
	h := &MsgpackHandle{WriteExt: true, ExtRegistry: reg}
	if err := reg.RegisterBytesExt(hotExtIntTyp, 11, hotExtA{}); err != nil {
		t.Fatal(err)
	}

	const workers = 4
	const iters = 200

	var wg sync.WaitGroup
	errCh := make(chan error, workers*2)

	// swapper: keep replacing the registration between two generations
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			if i%2 == 0 {
				_ = reg.RegisterBytesExt(hotExtIntTyp, 11, hotExtA{})
			} else {
				_ = reg.RegisterBytesExt(hotExtIntTyp, 12, hotExtB{})
			}
		}
	}()

	// workers: encode then decode each value within one pinned call each
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				in := hotExtInt(w*iters + i)
				var bs []byte
				if err := NewEncoderBytes(&bs, h).Encode(in); err != nil {
					errCh <- err
					return
				}
				// the blob must carry one of the two known tags
				switch bs[0] {
				case 0xd7: // fixext8
					if bs[1] != 11 && bs[1] != 12 {
						errCh <- errors.New("unexpected ext tag in blob")
						return
					}
				default:
					errCh <- errors.New("blob is not an ext encoding")
					return
				}
				var v interface{}
				if err := NewDecoderBytes(bs, h).Decode(&v); err != nil {
					errCh <- err
					return
				}
				switch out := v.(type) {
				case hotExtInt:
					// tag and ext always match within a generation,
					// so the value must survive the round trip
					if out != in {
						errCh <- errors.New("round trip corrupted value")
						return
					}
				case RawExt:
					// the registration was swapped between encode and
					// decode; degradation to RawExt is expected
					if out.Tag != 11 && out.Tag != 12 {
						errCh <- errors.New("RawExt with unexpected tag")
						return
					}
				default:
					errCh <- errors.New("unexpected decoded type")
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}
