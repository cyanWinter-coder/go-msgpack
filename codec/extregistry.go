// Copyright (c) 2012-2020 Ugorji Nwoke. All rights reserved.
// Use of this source code is governed by a MIT license found in the LICENSE file.

package codec

import (
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
)

// ErrUnknownExt is returned (wrapped) when decoding an extension tag that has
// no registered extension, and the handle's UnknownExtPolicy is UnknownExtError.
//
// Use errors.Is(err, ErrUnknownExt) to recognize it.
var ErrUnknownExt = errors.New("codec: no extension registered for tag")

// UnknownExtPolicy controls how decoding handles an extension tag for which
// no extension is registered (neither in the pinned ExtRegistry snapshot nor
// in the handle's static extensions).
type UnknownExtPolicy uint8

const (
	// UnknownExtRawExt decodes an unknown extension tag into a RawExt value.
	// This is the default, and matches the historical behavior.
	UnknownExtRawExt UnknownExtPolicy = iota

	// UnknownExtError makes decoding fail with an error matching ErrUnknownExt.
	UnknownExtError
)

// extRegistryGen is one immutable generation of an ExtRegistry.
// It is never modified after publication, so any number of in-flight
// Encode/Decode calls may pin and read it concurrently without locks.
type extRegistryGen struct {
	seq     uint64
	byRtid  map[uintptr]*extTypeTagFn // keyed by rt2id(rt)
	byRtidP map[uintptr]*extTypeTagFn // keyed by rt2id(reflect.PointerTo(rt))
	byTag   map[uint64]*extTypeTagFn
}

// ExtRegistry is a hot-swappable registry of extensions for use with
// MsgpackHandle.ExtRegistry.
//
// It is safe for concurrent use. Each call to Register or Unregister
// atomically publishes a new, immutable generation of the registry:
//
//   - re-registering a type replaces its previous registration in the new
//     generation, and
//   - snapshots taken before the change keep observing the old generation,
//     so in-flight Encode/Decode calls are never disturbed.
//
// A generation (and the extension instances it references) becomes eligible
// for garbage collection as soon as no snapshot references it anymore;
// publishing a new generation never blocks on in-flight calls.
type ExtRegistry struct {
	gen atomic.Pointer[extRegistryGen]
}

// NewExtRegistry returns an empty ExtRegistry.
func NewExtRegistry() *ExtRegistry {
	return new(ExtRegistry)
}

// load returns the current generation, initializing it on first use.
func (r *ExtRegistry) load() *extRegistryGen {
	g := r.gen.Load()
	if g == nil {
		g = &extRegistryGen{}
		if r.gen.CompareAndSwap(nil, g) {
			return g
		}
		return r.gen.Load()
	}
	return g
}

// Generation returns the sequence number of the current generation.
// It starts at 0 and increases by 1 with each successful Register/Unregister.
func (r *ExtRegistry) Generation() uint64 {
	return r.load().seq
}

// Register adds (or replaces) the extension for a type in the registry.
//
// The type must be a named type (not a pointer or interface), and cannot be
// one of the natively supported types (time.Time, Raw, RawExt). The tag must
// fit in a msgpack extension type byte (tag <= 0xff).
//
// Re-registering the same type replaces its previous registration: the new
// (tag, ext) pair takes effect for snapshots taken after this call, while
// snapshots pinned by in-flight calls keep using the previous registration.
//
// If two types are registered with the same tag, the most recent registration
// wins for tag-based lookups during decoding.
func (r *ExtRegistry) Register(rt reflect.Type, tag uint64, ext Ext) error {
	rtid, rtidptr, err := extRegistryTypeInfo(rt)
	if err != nil {
		return err
	}
	if ext == nil {
		return fmt.Errorf("codec: ExtRegistry.Register: nil Ext for type %v (use Unregister to remove a registration)", rt)
	}
	if tag > 0xff {
		return fmt.Errorf("codec: ExtRegistry.Register: tag must be <= 0xff; got: %v", tag)
	}
	entry := &extTypeTagFn{rtid: rtid, rtidptr: rtidptr, rt: rt, tag: tag, ext: ext}
	r.update(func(g *extRegistryGen) {
		g.byRtid[rtid] = entry
		g.byRtidP[rtidptr] = entry
		g.byTag[tag] = entry
	}, rtid)
	return nil
}

// RegisterBytesExt is like Register, but takes a BytesExt.
// Decode-side interface conversions (UpdateExt) are not supported by the
// resulting registration, mirroring MsgpackHandle.SetBytesExt.
func (r *ExtRegistry) RegisterBytesExt(rt reflect.Type, tag uint64, ext BytesExt) error {
	return r.Register(rt, tag, &extWrapper{ext, interfaceExtFailer{}})
}

// Unregister removes the registration for rt, if present.
//
// Snapshots pinned by in-flight calls keep the removed registration;
// snapshots taken after this call no longer see it.
func (r *ExtRegistry) Unregister(rt reflect.Type) {
	rtid, _, err := extRegistryTypeInfo(rt)
	if err != nil {
		return
	}
	r.update(func(g *extRegistryGen) {}, rtid)
}

// update atomically publishes a new generation derived from the current one.
// The registration for rtid (if any) is dropped from the new generation
// before fn installs the replacement entries.
func (r *ExtRegistry) update(fn func(g *extRegistryGen), rtid uintptr) {
	for {
		old := r.load()
		g := &extRegistryGen{
			seq:     old.seq + 1,
			byRtid:  make(map[uintptr]*extTypeTagFn, len(old.byRtid)+1),
			byRtidP: make(map[uintptr]*extTypeTagFn, len(old.byRtidP)+1),
			byTag:   make(map[uint64]*extTypeTagFn, len(old.byTag)+1),
		}
		for k, v := range old.byRtid {
			if k != rtid {
				g.byRtid[k] = v
			}
		}
		for k, v := range old.byRtidP {
			if v.rtid != rtid {
				g.byRtidP[k] = v
			}
		}
		for k, v := range old.byTag {
			if v.rtid != rtid {
				g.byTag[k] = v
			}
		}
		fn(g)
		if r.gen.CompareAndSwap(old, g) {
			return
		}
	}
}

// Snapshot pins the current generation of the registry.
//
// The returned snapshot is immutable and safe for concurrent use: it keeps
// observing exactly this generation of registrations, regardless of later
// Register/Unregister calls. Call Release when the snapshot is no longer
// needed, so the pinned extension instances can be reclaimed promptly.
func (r *ExtRegistry) Snapshot() *ExtSnapshot {
	return &ExtSnapshot{g: r.load()}
}

// extRegistryTypeInfo validates a type for extension registration and returns
// its rtid and the rtid of its pointer type, mirroring extHandle.SetExt.
func extRegistryTypeInfo(rt reflect.Type) (rtid, rtidptr uintptr, err error) {
	rk := rt.Kind()
	for rk == reflect.Pointer {
		rt = rt.Elem()
		rk = rt.Kind()
	}
	if rt.PkgPath() == "" || rk == reflect.Interface {
		err = fmt.Errorf("codec: ExtRegistry: takes named type, not a pointer or interface: %v", rt)
		return
	}
	rtid = rt2id(rt)
	switch rtid {
	case timeTypId, rawTypId, rawExtTypId:
		// all natively supported types, so cannot have an extension
		err = fmt.Errorf("codec: ExtRegistry: type %v is natively supported; cannot register an extension for it", rt)
		return
	}
	rtidptr = rt2id(reflect.PointerTo(rt))
	return
}

// ExtSnapshot is a pinned, immutable generation of an ExtRegistry.
//
// A snapshot is safe for concurrent use. Its release is deterministic and
// never blocks: Release just drops the snapshot's reference to the
// generation, making the pinned extension instances eligible for garbage
// collection; it never waits for other calls.
type ExtSnapshot struct {
	g *extRegistryGen
}

// Generation returns the registry generation this snapshot pinned.
func (s *ExtSnapshot) Generation() uint64 {
	if s.g == nil {
		return 0
	}
	return s.g.seq
}

// Len returns the number of type registrations visible in this snapshot.
func (s *ExtSnapshot) Len() int {
	if s.g == nil {
		return 0
	}
	return len(s.g.byRtid)
}

// Lookup returns the extension and tag registered for rt in this snapshot.
func (s *ExtSnapshot) Lookup(rt reflect.Type) (ext Ext, tag uint64, ok bool) {
	rtid, rtidptr, err := extRegistryTypeInfo(rt)
	if err != nil {
		return nil, 0, false
	}
	if v := s.getExt(rtid); v != nil {
		return v.ext, v.tag, true
	}
	if v := s.getExt(rtidptr); v != nil {
		return v.ext, v.tag, true
	}
	return nil, 0, false
}

// LookupTag returns the extension and type registered for tag in this snapshot.
func (s *ExtSnapshot) LookupTag(tag uint64) (ext Ext, rt reflect.Type, ok bool) {
	if v := s.getExtForTag(tag); v != nil {
		return v.ext, v.rt, true
	}
	return nil, nil, false
}

// Release drops the snapshot's reference to its pinned generation.
//
// It is idempotent and never blocks. After Release, all lookups on this
// snapshot report "not found"; the registry itself is unaffected.
func (s *ExtSnapshot) Release() {
	s.g = nil
}

// getExt mirrors extHandle.getExt: it matches a registration by the rtid of
// the registered type or of its pointer type.
func (s *ExtSnapshot) getExt(rtid uintptr) (v *extTypeTagFn) {
	g := s.g
	if g == nil {
		return nil
	}
	if v = g.byRtid[rtid]; v != nil {
		return
	}
	return g.byRtidP[rtid]
}

// getExtForTag mirrors extHandle.getExtForTag.
func (s *ExtSnapshot) getExtForTag(tag uint64) (v *extTypeTagFn) {
	g := s.g
	if g == nil {
		return nil
	}
	return g.byTag[tag]
}

// extCodecFn returns a codecFn for rt resolved from this snapshot,
// or nil if rt is not registered in the snapshot.
//
// The returned codecFn mirrors the extension branch of BasicHandle.fn, so
// registry-resolved types encode/decode exactly like statically registered
// ones. It is not cached across calls: a codecFn is cheap to build (the
// typeInfo is cached by the handle), and not caching keeps each call bound
// to exactly its pinned generation.
func (s *ExtSnapshot) extCodecFn(h *BasicHandle, rt reflect.Type) (fn *codecFn) {
	entry := s.getExt(rt2id(rt))
	if entry == nil {
		return nil
	}
	fn = new(codecFn)
	fi := &(fn.i)
	fi.ti = h.getTypeInfo(rt2id(rt), rt)
	fi.xfTag, fi.xfFn = entry.tag, entry.ext
	fn.fe = (*Encoder).ext
	fn.fd = (*Decoder).ext
	fi.addrF = true
	fi.addrD = true
	if rk := reflect.Kind(fi.ti.kind); rk == reflect.Struct || rk == reflect.Array {
		fi.addrE = true
	}
	return
}

// ---- Encoder/Decoder integration ----
//
// The pinned snapshot is per-call state, so it lives on the Encoder/Decoder
// (which are not safe for concurrent use), never on the shared Handle.

// pinExtSnap pins a snapshot of the handle's ExtRegistry (if configured) for
// the remainder of the current top-level call. It reports whether a snapshot
// was pinned; nested calls (e.g. from a Selfer) see extSnap already set and
// keep the outer call's snapshot.
func (e *Encoder) pinExtSnap() (pinned bool) {
	if e.extSnap != nil {
		return false
	}
	if h, ok := e.hh.(*MsgpackHandle); ok && h.ExtRegistry != nil {
		e.extSnap = h.ExtRegistry.Snapshot()
		return true
	}
	return false
}

func (e *Encoder) unpinExtSnap() { e.extSnap = nil }

func (d *Decoder) pinExtSnap() (pinned bool) {
	if d.extSnap != nil {
		return false
	}
	if h, ok := d.hh.(*MsgpackHandle); ok && h.ExtRegistry != nil {
		d.extSnap = h.ExtRegistry.Snapshot()
		return true
	}
	return false
}

func (d *Decoder) unpinExtSnap() { d.extSnap = nil }

// fnFor resolves the codecFn for rt, consulting the pinned extension
// registry snapshot (if any) before the handle's static configuration.
func (e *Encoder) fnFor(rt reflect.Type, checkFastpath, checkCodecSelfer bool) (fn *codecFn) {
	if e.extSnap != nil {
		if fn = e.extSnap.extCodecFn(e.h, rt); fn != nil {
			return
		}
	}
	return e.h.fn(rt, checkFastpath, checkCodecSelfer)
}

// fnFor resolves the codecFn for rt, consulting the pinned extension
// registry snapshot (if any) before the handle's static configuration.
func (d *Decoder) fnFor(rt reflect.Type, checkFastpath, checkCodecSelfer bool) (fn *codecFn) {
	if d.extSnap != nil {
		if fn = d.extSnap.extCodecFn(d.h, rt); fn != nil {
			return
		}
	}
	return d.h.fn(rt, checkFastpath, checkCodecSelfer)
}
