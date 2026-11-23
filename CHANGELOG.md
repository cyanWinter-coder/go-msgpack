## Unreleased

### Improvements

* codec: Canonical encoding now sorts `time.Time` map keys chronologically, and
  a failed `Encode`/`MustEncode` to a `[]byte` rolls the output buffer back, so
  errors (circular references, unencodable map keys or values, out-of-range
  times) never leave a partially written value behind.

### Changes

### Fixed

* codec: Canonical map encoding used the map type (not the key type) when
  checking for `time.Time` keys, so time-keyed maps never took the chronological
  sort path.
* codec: msgpack `EncodeTime` silently wrote nothing when
  `time.Time.MarshalBinary` failed (e.g. zone offset out of range) with
  `TimeNotBuiltin` set; it now returns a clear error.

### Security
