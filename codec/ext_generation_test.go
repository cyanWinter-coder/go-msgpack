// Copyright (c) 2012-2018 Ugorji Nwoke. All rights reserved.
// Use of this source code is governed by a MIT license found in the LICENSE file.

package codec

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

// These types are used to exercise extension registration generations.
// Each is a distinct named type, so each has its own extension id and type id.

type extGenInt int64
type extGenAlt int64
type extGenPtrAlias *extGenInt

// extGenAlias is a true Go type alias: it is the exact same type as extGenInt.
type extGenAlias = extGenInt

type extGenPair struct {
	A extGenInt
	B extGenAlt
}

// extGenGeneric is instantiated with distinct type arguments, giving several
// distinct named types that can be registered concurrently.
type extGenGeneric[T any] int64

var extGenGenericTypes = []reflect.Type{
	reflect.TypeOf(extGenGeneric[int](0)),
	reflect.TypeOf(extGenGeneric[int8](0)),
	reflect.TypeOf(extGenGeneric[int16](0)),
	reflect.TypeOf(extGenGeneric[int32](0)),
	reflect.TypeOf(extGenGeneric[int64](0)),
	reflect.TypeOf(extGenGeneric[uint](0)),
	reflect.TypeOf(extGenGeneric[uint8](0)),
	reflect.TypeOf(extGenGeneric[uint16](0)),
	reflect.TypeOf(extGenGeneric[uint32](0)),
	reflect.TypeOf(extGenGeneric[uint64](0)),
}

// extGenBytesExt encodes any signed integer kind as 8 big-endian bytes.
// The optional callback runs on every encode or decode, allowing a test to
// register a new extension from inside an in-flight operation.
type extGenBytesExt struct {
	cb func()
}

func (x *extGenBytesExt) WriteExt(v interface{}) []byte {
	if x.cb != nil {
		x.cb()
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	bs := make([]byte, 8)
	binary.BigEndian.PutUint64(bs, uint64(rv.Int()))
	return bs
}

func (x *extGenBytesExt) ReadExt(dst interface{}, bs []byte) {
	if x.cb != nil {
		x.cb()
	}
	rv := reflect.ValueOf(dst)
	for rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	rv.SetInt(int64(binary.BigEndian.Uint64(bs)))
}

func extGenMarshal(t *testing.T, h Handle, v interface{}) []byte {
	t.Helper()
	var out []byte
	if err := NewEncoderBytes(&out, h).Encode(v); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return out
}

func extGenUnmarshal(t *testing.T, h Handle, bs []byte, v interface{}) error {
	t.Helper()
	return NewDecoderBytes(bs, h).Decode(v)
}

func extGenRoundTrip(t *testing.T, h Handle, v extGenInt) {
	t.Helper()
	bs := extGenMarshal(t, h, v)
	var got extGenInt
	if err := extGenUnmarshal(t, h, bs, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != v {
		t.Fatalf("round trip: got %v, want %v", got, v)
	}
}

func TestExtRegistrationRejectsConflicts(t *testing.T) {
	h := &MsgpackHandle{}
	h.WriteExt = true

	t1 := reflect.TypeOf(extGenInt(0))
	t2 := reflect.TypeOf(extGenAlt(0))
	if err := h.SetBytesExt(t1, 10, &extGenBytesExt{}); err != nil {
		t.Fatalf("register %v: %v", t1, err)
	}

	for _, tc := range []struct {
		name string
		rt   reflect.Type
		tag  uint64
	}{
		{"duplicate id", t2, 10},
		{"duplicate type", t1, 11},
		{"duplicate pointer alias", reflect.PointerTo(t1), 12},
		{"duplicate alias type", reflect.TypeOf(extGenAlias(0)), 13},
		{"duplicate named pointer alias", reflect.TypeOf(extGenPtrAlias(nil)), 14},
	} {
		if err := h.SetBytesExt(tc.rt, tc.tag, &extGenBytesExt{}); err == nil {
			t.Fatalf("%s: expected registration of %v with id %v to fail", tc.name, tc.rt, tc.tag)
		}
	}

	// A rejected registration must leave the previously published generation
	// fully usable, with the original mapping intact.
	extGenRoundTrip(t, h, 42)
	if got := basicHandle(h).getExt(rt2id(t1)); got == nil || got.tag != 10 {
		t.Fatalf("expected extension for %v to still be registered with id 10, got %+v", t1, got)
	}

	// The type from the rejected registrations is still encoded without an
	// extension.
	bs := extGenMarshal(t, h, extGenAlt(7))
	if len(bs) != 1 || bs[0] != 7 {
		t.Fatalf("rejected registration changed encoding of %v: % x", t2, bs)
	}
}

func TestExtSnapshotAliasConflict(t *testing.T) {
	// Exercise the alias-conflict branch directly: publishing an entry whose
	// pointer alias is already mapped to another type must be rejected.
	s := newExtSnapshot(0)
	fnA := &extTypeTagFn{rtid: 1, rtidptr: 2, rt: reflect.TypeOf(extGenInt(0)), tag: 1}
	s1, err := s.withAdd(fnA)
	if err != nil {
		t.Fatalf("first registration: %v", err)
	}
	fnB := &extTypeTagFn{rtid: 3, rtidptr: 1, rt: reflect.TypeOf(extGenAlt(0)), tag: 2}
	if _, err = s1.withAdd(fnB); err == nil {
		t.Fatal("expected conflicting alias registration to fail")
	}
	if got := s1.getExt(2); got != fnA {
		t.Fatal("published snapshot was mutated by a failed registration")
	}
}

func TestExtGenerationIsolation(t *testing.T) {
	h := &MsgpackHandle{}
	h.WriteExt = true

	// Encoding once caches the resolved (non-extension) codec function against
	// the initial generation.
	plain := extGenMarshal(t, h, extGenInt(7))

	t1 := reflect.TypeOf(extGenInt(0))
	if err := h.SetBytesExt(t1, 20, &extGenBytesExt{}); err != nil {
		t.Fatalf("register: %v", err)
	}

	// A later operation must resolve against the new generation and therefore
	// see the extension, rather than reusing the cached function.
	withext := extGenMarshal(t, h, extGenInt(7))
	if bytes.Equal(plain, withext) {
		t.Fatalf("new generation reused a codec function cached by the old one: % x", withext)
	}
	var got extGenInt
	if err := extGenUnmarshal(t, h, withext, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != 7 {
		t.Fatalf("got %v, want 7", got)
	}
}

func TestExtInFlightOperationUsesOneGeneration(t *testing.T) {
	h := &MsgpackHandle{}
	h.WriteExt = true

	t1 := reflect.TypeOf(extGenInt(0))
	t2 := reflect.TypeOf(extGenAlt(0))
	var once sync.Once
	registerAlt := func() {
		once.Do(func() {
			if err := h.SetBytesExt(t2, 31, &extGenBytesExt{}); err != nil {
				t.Errorf("register from in-flight operation: %v", err)
			}
		})
	}
	if err := h.SetBytesExt(t1, 30, &extGenBytesExt{cb: registerAlt}); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Encoding A registers B halfway through this operation. The operation was
	// already pinned to a generation without B, so B must be encoded without
	// an extension.
	inFlight := extGenMarshal(t, h, extGenPair{A: 5, B: 6})

	// Decoding now uses the newest generation, where B is an extension. If B
	// had been encoded as a plain integer, decoding it as an extension fails.
	var p extGenPair
	if err := extGenUnmarshal(t, h, inFlight, &p); err == nil {
		t.Fatalf("in-flight operation mixed generations: B was encoded as an extension (decoded %+v)", p)
	}

	// The next operation sees the newly published generation and encodes both
	// fields as extensions.
	after := extGenMarshal(t, h, extGenPair{A: 5, B: 6})
	if bytes.Equal(inFlight, after) {
		t.Fatal("registration did not affect subsequent operations")
	}
	if err := extGenUnmarshal(t, h, after, &p); err != nil {
		t.Fatalf("decode after registration: %v", err)
	}
	if p.A != 5 || p.B != 6 {
		t.Fatalf("got %+v, want {A:5 B:6}", p)
	}
}

func TestExtInFlightDecodeUsesOneGeneration(t *testing.T) {
	producer := &MsgpackHandle{}
	producer.WriteExt = true
	t1 := reflect.TypeOf(extGenInt(0))
	t2 := reflect.TypeOf(extGenAlt(0))
	if err := producer.SetBytesExt(t1, 40, &extGenBytesExt{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := producer.SetBytesExt(t2, 41, &extGenBytesExt{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	bs := extGenMarshal(t, producer, []interface{}{extGenInt(3), extGenAlt(4)})

	consumer := &MsgpackHandle{}
	consumer.WriteExt = true
	var once sync.Once
	registerAlt := func() {
		once.Do(func() {
			if err := consumer.SetBytesExt(t2, 41, &extGenBytesExt{}); err != nil {
				t.Errorf("register from in-flight operation: %v", err)
			}
		})
	}
	if err := consumer.SetBytesExt(t1, 40, &extGenBytesExt{cb: registerAlt}); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Reading the first element registers the second type. This decode was
	// pinned before that registration, so the second element must be surfaced
	// as a RawExt rather than decoded with the new mapping.
	var out []interface{}
	dec := NewDecoderBytes(bs, consumer)
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d elements, want 2", len(out))
	}
	if v, ok := out[0].(extGenInt); !ok || v != 3 {
		t.Fatalf("element 0: got %#v, want extGenInt(3)", out[0])
	}
	if re, ok := out[1].(RawExt); !ok || re.Tag != 41 {
		t.Fatalf("element 1: got %#v, want RawExt with tag 41", out[1])
	}

	// A subsequent decode sees the new generation, so the second element is
	// decoded through its extension.
	dec.ResetBytes(bs)
	out = nil
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("decode after registration: %v", err)
	}
	if v, ok := out[1].(extGenAlt); !ok || v != 4 {
		t.Fatalf("element 1: got %#v, want extGenAlt(4)", out[1])
	}
}

func TestExtSnapshotSurvivesReleaseAndReset(t *testing.T) {
	h := &MsgpackHandle{}
	h.WriteExt = true
	t1 := reflect.TypeOf(extGenInt(0))
	if err := h.SetBytesExt(t1, 50, &extGenBytesExt{}); err != nil {
		t.Fatalf("register: %v", err)
	}

	var out []byte
	enc := NewEncoderBytes(&out, h)
	if err := enc.Encode(extGenInt(9)); err != nil {
		t.Fatalf("encode: %v", err)
	}
	first := append([]byte(nil), out...)
	enc.Release()
	if err := enc.Encode(extGenInt(9)); err != nil {
		t.Fatalf("encode after Release: %v", err)
	}
	enc.ResetBytes(&out)
	if err := enc.Encode(extGenInt(9)); err != nil {
		t.Fatalf("encode after ResetBytes: %v", err)
	}
	if !bytes.Equal(first, out) {
		t.Fatalf("encoding changed after Release/Reset: % x vs % x", first, out)
	}

	dec := NewDecoderBytes(first, h)
	var got extGenInt
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	dec.Release()
	dec.ResetBytes(first)
	got = 0
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("decode after Release/Reset: %v", err)
	}
	if got != 9 {
		t.Fatalf("got %v, want 9", got)
	}
}

func TestExtConcurrentRegistrationAndUse(t *testing.T) {
	h := &MsgpackHandle{}
	h.WriteExt = true
	t1 := reflect.TypeOf(extGenInt(0))
	if err := h.SetBytesExt(t1, 60, &extGenBytesExt{}); err != nil {
		t.Fatalf("register: %v", err)
	}

	var writers sync.WaitGroup
	var readers sync.WaitGroup
	stop := make(chan struct{})

	// Readers continuously encode/decode while registrations are published.
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				bs := extGenMarshal(t, h, extGenInt(11))
				var got extGenInt
				if err := extGenUnmarshal(t, h, bs, &got); err != nil {
					t.Errorf("concurrent decode: %v", err)
					return
				}
				if got != 11 {
					t.Errorf("concurrent round trip: got %v, want 11", got)
					return
				}
			}
		}()
	}

	// Registrations use distinct types and ids, so all must succeed.
	for i, rt := range extGenGenericTypes {
		writers.Add(1)
		go func(i int, rt reflect.Type) {
			defer writers.Done()
			if err := h.SetBytesExt(rt, uint64(100+i), &extGenBytesExt{}); err != nil {
				t.Errorf("register %v: %v", rt, err)
			}
		}(i, rt)
	}
	writers.Wait()
	close(stop)
	readers.Wait()

	// Every registration must be visible in the final generation.
	snap := basicHandle(h).getExtSnapshot()
	if got, want := snap.count(), 1+len(extGenGenericTypes); got != want {
		t.Fatalf("registered extensions: got %d, want %d", got, want)
	}
	extGenRoundTrip(t, h, 12)
}

func TestExtConcurrentDuplicateRegistration(t *testing.T) {
	h := &MsgpackHandle{}
	h.WriteExt = true

	var wg sync.WaitGroup
	var successes int32
	for _, rt := range extGenGenericTypes {
		wg.Add(1)
		go func(rt reflect.Type) {
			defer wg.Done()
			if err := h.SetBytesExt(rt, 77, &extGenBytesExt{}); err == nil {
				atomic.AddInt32(&successes, 1)
			}
		}(rt)
	}
	wg.Wait()

	if successes != 1 {
		t.Fatalf("registrations claiming the same id succeeded %d times, want exactly 1", successes)
	}
	if got := basicHandle(h).getExtSnapshot().count(); got != 1 {
		t.Fatalf("published snapshot has %d extensions, want 1", got)
	}
}

func TestExtDeregisterOnlyAffectsLaterOperations(t *testing.T) {
	h := &MsgpackHandle{}
	h.WriteExt = true
	t1 := reflect.TypeOf(extGenInt(0))
	ext := &extGenBytesExt{}
	if err := h.SetBytesExt(t1, 80, ext); err != nil {
		t.Fatalf("register: %v", err)
	}
	withExt := extGenMarshal(t, h, extGenInt(13))

	// Deregistering publishes a new generation, without touching the old one.
	if err := h.SetExt(t1, 80, nil); err != nil {
		t.Fatalf("deregister: %v", err)
	}
	withoutExt := extGenMarshal(t, h, extGenInt(13))
	if bytes.Equal(withExt, withoutExt) {
		t.Fatal("deregistration did not affect later operations")
	}

	// Re-registering the same type and id is allowed after removal.
	if err := h.SetBytesExt(t1, 80, ext); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	extGenRoundTrip(t, h, 14)
}
