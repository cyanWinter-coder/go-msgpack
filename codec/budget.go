// Copyright (c) 2012-2018 Ugorji Nwoke. All rights reserved.
// Use of this source code is governed by a MIT license found in the LICENSE file.

package codec

import (
	"fmt"
	"io"
)

// checkContainerBudget validates a declared container length before the
// decoder allocates its backing slice or map.
//
// A negative length means that the format does not use a fixed length (for
// example, an indefinitely delimited JSON container), so no declaration check
// is possible. For length-prefixed formats, every array value needs at least
// one wire byte and every map entry needs at least a key and a value byte.
// When the complete input is addressable, that lower bound catches hostile
// headers such as map32(MaxUint32) followed by only a handful of bytes.
func (d *Decoder) checkContainerBudget(vt valueType, length int) {
	if length < 0 {
		return
	}

	if max := d.h.MaxContainerLen; max > 0 && length > max {
		kind := "array"
		if vt == valueTypeMap {
			kind = "map"
		}
		panic(fmt.Errorf("%w: %s declares %d entries, limit is %d",
			ErrContainerLengthExceeded, kind, length, max))
	}

	remaining, known := d.r.remainingLen()
	if !known {
		return
	}

	minBytes := uint64(length)
	if vt == valueTypeMap {
		// Keys and values are each encoded as at least one byte.
		minBytes *= 2
	}
	if minBytes > uint64(remaining) {
		// Panic the bare sentinel. The panic-to-error layer preserves
		// io.EOF/io.ErrUnexpectedEOF values without wrapping them, so callers
		// can compare directly as well as use errors.Is.
		panic(io.ErrUnexpectedEOF)
	}
}

func (z *bytesDecReader) remainingLen() int {
	return len(z.b) - int(z.c)
}

// remainingLen reports the total number of unread bytes when the underlying
// reader exposes its length. Generic io.Reader streams do not expose this and
// return ok=false; their normal read paths still reject short input.
func (z *decReaderSwitch) remainingLen() (remaining int, ok bool) {
	if z.bytes {
		return z.rb.remainingLen(), true
	}
	if z.bufio && z.bi != nil {
		n, known := readerRemainingLen(z.bi.r)
		if !known {
			return 0, false
		}
		return len(z.bi.buf) - int(z.bi.c) + n, true
	}
	if z.ri != nil {
		return readerRemainingLen(z.ri.r)
	}
	return 0, false
}

type lenReader interface {
	Len() int
}

func maxDecoderInt() int {
	return int(^uint(0) >> 1)
}

func readerRemainingLen(r io.Reader) (int, bool) {
	lr, ok := r.(lenReader)
	if !ok {
		return 0, false
	}
	return lr.Len(), true
}
