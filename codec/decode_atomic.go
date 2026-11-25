// Copyright (c) 2012-2018 Ugorji Nwoke. All rights reserved.
// Use of this source code is governed by a MIT license found in the LICENSE file.

package codec

import "reflect"

// snapshotDecodeDestination captures the caller's value so a failed decode can
// roll the top-level destination back to its pre-Decode state. Decoding still
// happens directly against v, preserving the library's in-place update and
// nested-value identity behavior on success.
//
// The snapshot is deep for ordinary data kinds. Chans, funcs and
// unsafe.Pointers cannot be snapshotted portably and are shared.
func snapshotDecodeDestination(v interface{}) (rollback func()) {
	if rv, ok := v.(reflect.Value); ok && rv.IsValid() && rv.CanSet() {
		seen := make(map[cloneVisitKey]reflect.Value)
		snapshot := reflect.New(rv.Type()).Elem()
		snapshot.Set(deepCloneReflect(rv, seen))
		return func() {
			defer func() { _ = recover() }()
			rv.Set(snapshot)
		}
	}

	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Ptr || rv.IsNil() || !rv.Elem().IsValid() || !rv.Elem().CanSet() {
		return func() {}
	}

	seen := make(map[cloneVisitKey]reflect.Value)
	snapshot := reflect.New(rv.Elem().Type()).Elem()
	snapshot.Set(deepCloneReflect(rv.Elem(), seen))

	return func() {
		// A sticky/failed decode may have left values that cannot be assigned
		// in unusual reflection configurations. Rollback is best-effort for
		// those cases; ordinary exported destinations are always restored.
		defer func() { _ = recover() }()
		rv.Elem().Set(snapshot)
	}
}

type cloneVisitKey struct {
	t reflect.Type
	p uintptr
}

func deepCloneReflect(v reflect.Value, seen map[cloneVisitKey]reflect.Value) reflect.Value {
	if !v.IsValid() {
		return v
	}

	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		c := deepCloneReflect(v.Elem(), seen)
		out := reflect.New(v.Type()).Elem()
		out.Set(c)
		return out

	case reflect.Ptr:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		key := cloneVisitKey{t: v.Type(), p: v.Pointer()}
		if c, ok := seen[key]; ok {
			return c
		}
		out := reflect.New(v.Type().Elem())
		seen[key] = out
		out.Elem().Set(deepCloneReflect(v.Elem(), seen))
		return out

	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		if v.Len() > 0 {
			key := cloneVisitKey{t: v.Type(), p: v.Pointer()}
			if c, ok := seen[key]; ok {
				return c
			}
			out := reflect.MakeSlice(v.Type(), v.Len(), v.Cap())
			seen[key] = out
			for i := 0; i < v.Len(); i++ {
				out.Index(i).Set(deepCloneReflect(v.Index(i), seen))
			}
			return out
		}
		out := reflect.MakeSlice(v.Type(), 0, v.Cap())
		return out

	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		key := cloneVisitKey{t: v.Type(), p: v.Pointer()}
		if c, ok := seen[key]; ok {
			return c
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		seen[key] = out
		iter := v.MapRange()
		for iter.Next() {
			// Map keys are immutable while they remain in the map. Sharing the
			// key preserves pointer identity and comparable-key semantics.
			out.SetMapIndex(iter.Key(), deepCloneReflect(iter.Value(), seen))
		}
		return out

	case reflect.Array:
		out := reflect.New(v.Type()).Elem()
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(deepCloneReflect(v.Index(i), seen))
		}
		return out

	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		// A whole-value Set also copies unexported fields. Fields that are not
		// settable cannot be independently replaced, but the struct itself is a
		// fresh copy.
		out.Set(v)
		for i := 0; i < v.NumField(); i++ {
			f := out.Field(i)
			if !f.CanSet() {
				continue
			}
			switch f.Kind() {
			case reflect.Ptr, reflect.Interface, reflect.Slice, reflect.Map, reflect.Array, reflect.Struct:
				f.Set(deepCloneReflect(f, seen))
			}
		}
		return out

	default:
		// Scalars, chans, funcs and unsafe.Pointers are copied by value or
		// intentionally shared.
		return v
	}
}
