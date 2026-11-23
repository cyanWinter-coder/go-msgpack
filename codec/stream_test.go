// Copyright (c) 2012-2018 Ugorji Nwoke. All rights reserved.
// Use of this source code is governed by a MIT license found in the LICENSE file.

package codec

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// streamFrame encodes v and returns it as one length-prefixed frame.
func streamFrame(t *testing.T, h Handle, v interface{}) []byte {
	t.Helper()
	var body []byte
	if err := NewEncoderBytes(&body, h).Encode(v); err != nil {
		t.Fatalf("encode frame body: %v", err)
	}
	out := make([]byte, streamHeaderLen+len(body))
	binary.BigEndian.PutUint32(out, uint32(len(body)))
	copy(out[streamHeaderLen:], body)
	return out
}

// trickleReader returns at most one byte per Read, exercising the
// decoder against short reads from the underlying reader.
type trickleReader struct {
	r io.Reader
}

func (t trickleReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return t.r.Read(p)
}

// countingReader counts Read calls so tests can verify that a failed
// decoder stops consuming input.
type countingReader struct {
	r     io.Reader
	reads int
}

func (c *countingReader) Read(p []byte) (int, error) {
	c.reads++
	return c.r.Read(p)
}

// cancelReader returns err once n bytes have been served, simulating
// cancellation of the underlying stream.
type cancelReader struct {
	r   io.Reader
	n   int
	err error
}

func (c *cancelReader) Read(p []byte) (int, error) {
	if c.n <= 0 {
		return 0, c.err
	}
	if len(p) > c.n {
		p = p[:c.n]
	}
	n, err := c.r.Read(p)
	c.n -= n
	return n, err
}

func TestStreamDecoderConsecutiveObjects(t *testing.T) {
	h := &MsgpackHandle{}
	var stream []byte
	stream = append(stream, streamFrame(t, h, "one")...)
	stream = append(stream, streamFrame(t, h, []int{2, 3, 5})...)
	stream = append(stream, streamFrame(t, h, map[string]int{"seven": 7})...)

	d := NewStreamDecoder(bytes.NewReader(stream), h)

	var s string
	if err := d.ReadNext(&s); err != nil {
		t.Fatalf("read 1: %v", err)
	}
	if s != "one" {
		t.Fatalf("read 1: got %q", s)
	}

	var ints []int
	if err := d.ReadNext(&ints); err != nil {
		t.Fatalf("read 2: %v", err)
	}
	if len(ints) != 3 || ints[0] != 2 || ints[1] != 3 || ints[2] != 5 {
		t.Fatalf("read 2: got %v", ints)
	}

	var m map[string]int
	if err := d.ReadNext(&m); err != nil {
		t.Fatalf("read 3: %v", err)
	}
	if m["seven"] != 7 {
		t.Fatalf("read 3: got %v", m)
	}

	// EOF at a frame boundary is a normal end of stream, and repeats.
	var extra interface{}
	if err := d.ReadNext(&extra); err != io.EOF {
		t.Fatalf("read 4: want io.EOF, got %v", err)
	}
	if err := d.ReadNext(&extra); err != io.EOF {
		t.Fatalf("read 5: want io.EOF, got %v", err)
	}
}

func TestStreamDecoderShortReads(t *testing.T) {
	h := &MsgpackHandle{}
	var stream []byte
	stream = append(stream, streamFrame(t, h, "trickle")...)
	stream = append(stream, streamFrame(t, h, uint64(42))...)

	d := NewStreamDecoder(trickleReader{bytes.NewReader(stream)}, h)

	var s string
	if err := d.ReadNext(&s); err != nil {
		t.Fatalf("read 1: %v", err)
	}
	if s != "trickle" {
		t.Fatalf("read 1: got %q", s)
	}

	var n uint64
	if err := d.ReadNext(&n); err != nil {
		t.Fatalf("read 2: %v", err)
	}
	if n != 42 {
		t.Fatalf("read 2: got %d", n)
	}

	var extra interface{}
	if err := d.ReadNext(&extra); err != io.EOF {
		t.Fatalf("read 3: want io.EOF, got %v", err)
	}
}

func TestStreamDecoderTruncatedHeader(t *testing.T) {
	h := &MsgpackHandle{}
	stream := streamFrame(t, h, "complete")
	// Append only part of the next frame's length header.
	stream = append(stream, 0x00, 0x00)

	cr := &countingReader{r: bytes.NewReader(stream)}
	d := NewStreamDecoder(cr, h)

	var s string
	if err := d.ReadNext(&s); err != nil {
		t.Fatalf("read 1: %v", err)
	}
	if s != "complete" {
		t.Fatalf("read 1: got %q", s)
	}

	err := d.ReadNext(&s)
	if !errors.Is(err, ErrStreamTruncatedHeader) {
		t.Fatalf("read 2: want ErrStreamTruncatedHeader, got %v", err)
	}

	// Sticky: same error, no further consumption of the input.
	reads := cr.reads
	for i := 0; i < 3; i++ {
		if got := d.ReadNext(&s); got != err {
			t.Fatalf("sticky read %d: want %v, got %v", i, err, got)
		}
	}
	if cr.reads != reads {
		t.Fatalf("failed decoder consumed input: reads %d -> %d", reads, cr.reads)
	}
}

func TestStreamDecoderTruncatedBody(t *testing.T) {
	h := &MsgpackHandle{}
	good := streamFrame(t, h, "complete")
	frame := streamFrame(t, h, "this body is cut short")
	// Keep the full length header, drop the tail of the body.
	stream := append(good, frame[:len(frame)-3]...)

	cr := &countingReader{r: bytes.NewReader(stream)}
	d := NewStreamDecoder(cr, h)

	var s string
	if err := d.ReadNext(&s); err != nil {
		t.Fatalf("read 1: %v", err)
	}
	if s != "complete" {
		t.Fatalf("read 1: got %q", s)
	}

	err := d.ReadNext(&s)
	if !errors.Is(err, ErrStreamTruncatedBody) {
		t.Fatalf("read 2: want ErrStreamTruncatedBody, got %v", err)
	}

	reads := cr.reads
	if got := d.ReadNext(&s); got != err {
		t.Fatalf("sticky read: want %v, got %v", err, got)
	}
	if cr.reads != reads {
		t.Fatalf("failed decoder consumed input: reads %d -> %d", reads, cr.reads)
	}
}

func TestStreamDecoderFailedObjectNotExposed(t *testing.T) {
	h := &MsgpackHandle{}

	// A frame whose body is internally truncated msgpack: an array
	// header announcing 3 elements followed by only one. The length
	// header matches the body, so the failure surfaces during decode.
	body := []byte{0x93, 0x01}
	stream := make([]byte, streamHeaderLen+len(body))
	binary.BigEndian.PutUint32(stream, uint32(len(body)))
	copy(stream[streamHeaderLen:], body)

	d := NewStreamDecoder(bytes.NewReader(stream), h)

	target := []int{9, 9, 9}
	err := d.ReadNext(&target)
	if err == nil {
		t.Fatal("want decode failure, got nil")
	}
	if len(target) != 3 || target[0] != 9 || target[1] != 9 || target[2] != 9 {
		t.Fatalf("failed object partially exposed: %v", target)
	}

	// The decode failure is sticky too.
	if got := d.ReadNext(&target); got != err {
		t.Fatalf("sticky read: want %v, got %v", err, got)
	}
}

func TestStreamDecoderCancel(t *testing.T) {
	h := &MsgpackHandle{}
	var stream []byte
	stream = append(stream, streamFrame(t, h, "before cancel")...)
	stream = append(stream, streamFrame(t, h, "after cancel")...)

	first := streamFrame(t, h, "before cancel")
	cr := &countingReader{r: &cancelReader{
		r:   bytes.NewReader(stream),
		n:   len(first) + streamHeaderLen, // cancel partway into frame 2
		err: context.Canceled,
	}}
	d := NewStreamDecoder(cr, h)

	var s string
	if err := d.ReadNext(&s); err != nil {
		t.Fatalf("read 1: %v", err)
	}
	if s != "before cancel" {
		t.Fatalf("read 1: got %q", s)
	}

	err := d.ReadNext(&s)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("read 2: want context.Canceled, got %v", err)
	}

	reads := cr.reads
	if got := d.ReadNext(&s); got != err {
		t.Fatalf("sticky read: want %v, got %v", err, got)
	}
	if cr.reads != reads {
		t.Fatalf("failed decoder consumed input: reads %d -> %d", reads, cr.reads)
	}
}

func TestStreamDecoderReset(t *testing.T) {
	h := &MsgpackHandle{}

	// Truncated stream puts the decoder into its failed state.
	truncated := streamFrame(t, h, "cut")[:streamHeaderLen+1]
	d := NewStreamDecoder(bytes.NewReader(truncated), h)

	var s string
	err := d.ReadNext(&s)
	if !errors.Is(err, ErrStreamTruncatedBody) {
		t.Fatalf("want ErrStreamTruncatedBody, got %v", err)
	}

	// Reset clears the failure state and buffered data.
	d.Reset(bytes.NewReader(streamFrame(t, h, "recovered")))
	if err := d.ReadNext(&s); err != nil {
		t.Fatalf("read after reset: %v", err)
	}
	if s != "recovered" {
		t.Fatalf("read after reset: got %q", s)
	}

	var extra interface{}
	if err := d.ReadNext(&extra); err != io.EOF {
		t.Fatalf("final read: want io.EOF, got %v", err)
	}
}

func TestStreamDecoderFrameTooLarge(t *testing.T) {
	h := &MsgpackHandle{}
	stream := make([]byte, streamHeaderLen)
	binary.BigEndian.PutUint32(stream, streamDefMaxFrameLen+1)

	d := NewStreamDecoder(bytes.NewReader(stream), h)

	var v interface{}
	err := d.ReadNext(&v)
	if !errors.Is(err, ErrStreamFrameTooLarge) {
		t.Fatalf("want ErrStreamFrameTooLarge, got %v", err)
	}
	if got := d.ReadNext(&v); got != err {
		t.Fatalf("sticky read: want %v, got %v", err, got)
	}
}
