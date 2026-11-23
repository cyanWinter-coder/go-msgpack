// Copyright (c) 2012-2018 Ugorji Nwoke. All rights reserved.
// Use of this source code is governed by a MIT license found in the LICENSE file.

package codec

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"reflect"
)

// Stable error categories returned by StreamDecoder.ReadNext.
//
// These are sentinel values: use errors.Is to classify a failure.
// Once ReadNext has returned one of these (or any other non-EOF error),
// the StreamDecoder is in a failed state and every subsequent ReadNext
// returns the same error without consuming any further input, until
// Reset is called.
var (
	// ErrStreamTruncatedHeader reports that the stream ended in the
	// middle of a frame length header.
	ErrStreamTruncatedHeader = errors.New("codec: stream truncated in length header")

	// ErrStreamTruncatedBody reports that the stream ended in the
	// middle of a frame value body.
	ErrStreamTruncatedBody = errors.New("codec: stream truncated in value body")

	// ErrStreamFrameTooLarge reports that a frame length header
	// announced a body larger than the configured maximum.
	ErrStreamFrameTooLarge = errors.New("codec: stream frame length exceeds maximum")
)

// streamDefMaxFrameLen is the default maximum frame body length
// accepted by a StreamDecoder. It guards against corrupt or hostile
// length headers causing huge allocations.
const streamDefMaxFrameLen = 1 << 24 // 16 MiB

// streamHeaderLen is the number of bytes in a frame length header.
const streamHeaderLen = 4

// StreamDecoder decodes a stream of length-prefixed frames, where each
// frame is a 4-byte big-endian uint32 length header followed by exactly
// that many bytes holding one value encoded with the decoder's Handle.
//
// Failure semantics are sticky:
//   - Values fully decoded before a failure are delivered normally.
//   - A failing ReadNext never partially modifies the target value.
//   - After a failure, ReadNext returns the same error and consumes no
//     further input, until Reset is called.
//   - io.EOF at a frame boundary is a normal end of stream; io.EOF in
//     the middle of a frame is reported as ErrStreamTruncatedHeader or
//     ErrStreamTruncatedBody.
type StreamDecoder struct {
	r   io.Reader
	h   Handle
	buf []byte // reusable frame body buffer
	err error  // sticky failure; nil while healthy

	// MaxFrameLen is the maximum accepted frame body length in bytes.
	// If zero, streamDefMaxFrameLen is used.
	MaxFrameLen int
}

// NewStreamDecoder returns a StreamDecoder reading length-prefixed
// frames from r, decoding each frame body with Handle h.
func NewStreamDecoder(r io.Reader, h Handle) *StreamDecoder {
	return &StreamDecoder{r: r, h: h}
}

// ReadNext decodes the next frame in the stream into the value pointed
// to by v.
//
// It returns io.EOF when the stream ends cleanly at a frame boundary.
// Any other error puts the decoder into its failed state: subsequent
// calls return the same error without reading from the underlying
// reader, and v is left unmodified by the failing call.
func (d *StreamDecoder) ReadNext(v interface{}) error {
	if d.err != nil {
		return d.err
	}

	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		// Usage error, not a stream error: do not poison the decoder.
		return errors.New("codec: StreamDecoder.ReadNext requires a non-nil pointer")
	}

	var hdr [streamHeaderLen]byte
	if _, err := io.ReadFull(d.r, hdr[:]); err != nil {
		if err == io.EOF {
			// Clean end of stream at a frame boundary.
			return io.EOF
		}
		if err == io.ErrUnexpectedEOF {
			return d.fail(fmt.Errorf("%w", ErrStreamTruncatedHeader))
		}
		return d.fail(err)
	}

	frameLen := binary.BigEndian.Uint32(hdr[:])
	if max := d.maxFrameLen(); uint64(frameLen) > uint64(max) {
		return d.fail(fmt.Errorf("%w: %d > %d", ErrStreamFrameTooLarge, frameLen, max))
	}

	if cap(d.buf) < int(frameLen) {
		d.buf = make([]byte, frameLen)
	}
	d.buf = d.buf[:frameLen]
	if _, err := io.ReadFull(d.r, d.buf); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return d.fail(fmt.Errorf("%w", ErrStreamTruncatedBody))
		}
		return d.fail(err)
	}

	// Decode into a temporary value so a failed frame never partially
	// modifies the caller's target.
	tmp := reflect.New(rv.Elem().Type())
	dec := NewDecoderBytes(d.buf, d.h)
	if err := dec.Decode(tmp.Interface()); err != nil {
		return d.fail(err)
	}
	rv.Elem().Set(tmp.Elem())
	return nil
}

// Reset clears the failure state and any buffered frame data, and
// reattaches the decoder to r. If r is nil, only the failure state
// and buffer are cleared.
func (d *StreamDecoder) Reset(r io.Reader) {
	d.err = nil
	d.buf = d.buf[:0]
	if r != nil {
		d.r = r
	}
}

// fail records the sticky failure and returns it.
func (d *StreamDecoder) fail(err error) error {
	d.err = err
	return err
}

func (d *StreamDecoder) maxFrameLen() int {
	if d.MaxFrameLen > 0 {
		return d.MaxFrameLen
	}
	return streamDefMaxFrameLen
}
