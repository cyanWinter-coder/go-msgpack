## Unreleased

### Improvements

* codec: Add `ExtRegistry`, a generation-based, hot-swappable extension registry for `MsgpackHandle`. Each Encode/Decode call pins one immutable registry snapshot, so in-flight calls are never disturbed by re-registration, and unknown extension tags can optionally fail with the recognizable `ErrUnknownExt` via the new `MsgpackHandle.UnknownExtPolicy` option.

### Changes

### Fixed

### Security
