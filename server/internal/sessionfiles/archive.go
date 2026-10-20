package sessionfiles

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"
	"unicode/utf8"
)

// ValidateArchive checks that the zip is OZX's session directory and
// nothing else, so that unzipping it can only create
// <recording>/... (no path outside it, no link, no device file), whatever
// the device sent: every entry is under "<recording>/", relative, without
// "..", backslashes or a drive letter, a regular file or a directory;
// "<recording>/session.json" exists; the uncompressed total is bounded
// (zip bombs). Returns the number of files and their uncompressed bytes.
// Only the central directory is read (the content's integrity is the
// SHA-256 the device computed).
func ValidateArchive(r io.ReaderAt, size int64, recording string) (int, uint64, error) {
	if !ValidRecordingName(recording) {
		return 0, 0, fmt.Errorf("invalid recording name")
	}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		var se *storeReadError
		if errors.As(err, &se) {
			return 0, 0, err
		}
		return 0, 0, fmt.Errorf("not a zip archive: %v", err)
	}
	if len(zr.File) > maxArchiveEntries {
		return 0, 0, fmt.Errorf("%d entries; at most %d", len(zr.File), maxArchiveEntries)
	}
	root := recording + "/"
	var files int
	var raw uint64
	hasSession := false
	seen := map[string]bool{}
	for _, f := range zr.File {
		name := f.Name
		if !utf8.ValidString(name) || len(name) > 512 || strings.ContainsAny(name, "\\\x00:") {
			return 0, 0, fmt.Errorf("entry %q: invalid name", name)
		}
		if !strings.HasPrefix(name, root) {
			return 0, 0, fmt.Errorf("entry %q is outside %s", name, root)
		}
		isDir := strings.HasSuffix(name, "/")
		clean := path.Clean(name)
		if isDir {
			clean += "/"
		}
		if clean != name || strings.Contains(name, "//") {
			return 0, 0, fmt.Errorf("entry %q is not a clean relative path", name)
		}
		for _, seg := range strings.Split(strings.TrimSuffix(name, "/"), "/") {
			if seg == "" || seg == "." || seg == ".." {
				return 0, 0, fmt.Errorf("entry %q has an empty, . or .. segment", name)
			}
		}
		if seen[name] {
			return 0, 0, fmt.Errorf("entry %q appears twice", name)
		}
		seen[name] = true
		mode := f.Mode()
		if isDir {
			if !mode.IsDir() && mode.Type() != 0 {
				return 0, 0, fmt.Errorf("entry %q: not a directory", name)
			}
			continue
		}
		if !mode.IsRegular() {
			return 0, 0, fmt.Errorf("entry %q is not a regular file (%v)", name, mode.Type())
		}
		if f.Method != zip.Store && f.Method != zip.Deflate {
			return 0, 0, fmt.Errorf("entry %q: unsupported compression method %d", name, f.Method)
		}
		files++
		raw += f.UncompressedSize64
		if name == root+"session.json" {
			hasSession = true
		}
	}
	if !hasSession {
		return 0, 0, fmt.Errorf("%ssession.json is missing", root)
	}
	// Extractors that stream the archive read the local headers, not the
	// central directory: their names must be the same (checked above).
	if err := checkLocalHeaders(r, size); err != nil {
		return 0, 0, err
	}
	if limit := uint64(size) * maxRawPerArchive; raw > limit && raw > 64<<20 {
		return 0, 0, fmt.Errorf("uncompressed size %d is more than %d times the archive", raw, maxRawPerArchive)
	}
	return files, raw, nil
}

// storeReadError: reading the archive back from the object store failed
// (retryable), as opposed to the archive being wrong.
type storeReadError struct{ err error }

func (e *storeReadError) Error() string { return "object store read: " + e.err.Error() }
func (e *storeReadError) Unwrap() error { return e.err }

// objectReaderAt reads an object through ranged GETs in 256 KiB blocks,
// keeping the last blocks (the central directory is read sequentially).
type objectReaderAt struct {
	ctx context.Context
	obj Objects
	key string

	mu     sync.Mutex
	blocks map[int64][]byte
	order  []int64
}

const readBlock = 256 << 10

func newObjectReaderAt(ctx context.Context, obj Objects, key string) *objectReaderAt {
	return &objectReaderAt{ctx: ctx, obj: obj, key: key, blocks: map[int64][]byte{}}
}

func (r *objectReaderAt) block(i int64) ([]byte, error) {
	r.mu.Lock()
	b, ok := r.blocks[i]
	r.mu.Unlock()
	if ok {
		return b, nil
	}
	b, err := r.obj.GetRange(r.ctx, r.key, i*readBlock, readBlock)
	if err != nil {
		return nil, &storeReadError{err}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.order) >= 64 {
		delete(r.blocks, r.order[0])
		r.order = r.order[1:]
	}
	r.blocks[i] = b
	r.order = append(r.order, i)
	return b, nil
}

func (r *objectReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n := 0
	for n < len(p) {
		pos := off + int64(n)
		b, err := r.block(pos / readBlock)
		if err != nil {
			return n, err
		}
		in := pos % readBlock
		if in >= int64(len(b)) {
			return n, io.EOF
		}
		n += copy(p[n:], b[in:])
		if len(b) < readBlock && n < len(p) {
			return n, io.EOF // the object's last block: nothing after it
		}
	}
	return n, nil
}

// checkLocalHeaders compares every entry's local header name with its
// central directory name (no ZIP64: the SDK never writes it, and archives
// are far below 4 GiB). A mismatch, a missing header or an absurd extra
// field is an invalid archive.
func checkLocalHeaders(r io.ReaderAt, size int64) error {
	tailLen := int64(65557)
	if tailLen > size {
		tailLen = size
	}
	tail := make([]byte, tailLen)
	if _, err := r.ReadAt(tail, size-tailLen); err != nil && err != io.EOF {
		return err
	}
	eocd := bytes.LastIndex(tail, []byte{0x50, 0x4b, 0x05, 0x06})
	if eocd < 0 || len(tail)-eocd < 22 {
		return fmt.Errorf("no end of central directory")
	}
	le := binary.LittleEndian
	entries := int(le.Uint16(tail[eocd+10:]))
	cdSize := int64(le.Uint32(tail[eocd+12:]))
	cdOff := int64(le.Uint32(tail[eocd+16:]))
	if cdOff == 0xFFFFFFFF || cdSize == 0xFFFFFFFF || entries == 0xFFFF || cdOff+cdSize > size {
		return fmt.Errorf("ZIP64 or inconsistent central directory")
	}
	cd := make([]byte, cdSize)
	if _, err := r.ReadAt(cd, cdOff); err != nil && err != io.EOF {
		return err
	}
	pos := 0
	for i := 0; i < entries; i++ {
		if pos+46 > len(cd) || le.Uint32(cd[pos:]) != 0x02014b50 {
			return fmt.Errorf("central directory entry %d unreadable", i)
		}
		nameLen := int(le.Uint16(cd[pos+28:]))
		extraLen := int(le.Uint16(cd[pos+30:]))
		commentLen := int(le.Uint16(cd[pos+32:]))
		off := int64(le.Uint32(cd[pos+42:]))
		if pos+46+nameLen > len(cd) {
			return fmt.Errorf("central directory entry %d cut", i)
		}
		name := cd[pos+46 : pos+46+nameLen]
		if off+30+int64(nameLen) > size {
			return fmt.Errorf("entry %q: local header outside the archive", name)
		}
		local := make([]byte, 30+nameLen)
		if _, err := r.ReadAt(local, off); err != nil && err != io.EOF {
			return err
		}
		if le.Uint32(local) != 0x04034b50 {
			return fmt.Errorf("entry %q: no local header at its offset", name)
		}
		if int(le.Uint16(local[26:])) != nameLen || !bytes.Equal(local[30:], name) {
			return fmt.Errorf("entry %q: the local header names another file", name)
		}
		if le.Uint16(local[28:]) > 1024 {
			return fmt.Errorf("entry %q: local extra field too long", name)
		}
		pos += 46 + nameLen + extraLen + commentLen
	}
	return nil
}
