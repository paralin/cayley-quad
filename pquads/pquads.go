// Package pquads implements Cayley-specific protobuf-based quads format.
package pquads

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/cayleygraph/quad"
	"github.com/cayleygraph/quad/pquads/pio"
	"google.golang.org/protobuf/proto"
)

var DefaultMaxSize = 1024 * 1024

const currentVersion = 1

var magic = [4]byte{0, 'p', 'q', 0}

const ContentType = "application/x-protobuf"

func init() {
	quad.RegisterFormat(quad.Format{
		Name: "pquads", Binary: true,
		Ext:            []string{".pq"},
		Mime:           []string{ContentType, "application/octet-stream"},
		Writer:         func(w io.Writer) quad.WriteCloser { return NewWriter(w, nil) },
		Reader:         func(r io.Reader) quad.ReadCloser { return NewReader(r, DefaultMaxSize) },
		MarshalValue:   MarshalValue,
		UnmarshalValue: UnmarshalValue,
	})
}

type Writer struct {
	pw      pio.Writer
	max     int
	err     error
	opts    Options
	s, p, o quad.Value
	cl      io.Closer
}

type Options struct {
	// Full can be set to disable quad values compaction.
	//
	// This will increase files size, but skip will work faster by omitting unmarshal entirely.
	Full bool
	// Strict can be set to only marshal quads allowed by RDF spec.
	Strict bool
}

// NewWriter creates protobuf quads encoder.
func NewWriter(w io.Writer, opts *Options) *Writer {
	// Write file magic and version
	buf := make([]byte, 8)
	copy(buf[:4], magic[:])
	binary.LittleEndian.PutUint32(buf[4:], currentVersion)
	if _, err := w.Write(buf); err != nil {
		return &Writer{err: err}
	}
	pw := pio.NewWriter(w)
	if opts == nil {
		opts = &Options{}
	}
	// Write options header
	_, err := pw.WriteMsg(&Header{
		Full:      opts.Full,
		NotStrict: !opts.Strict,
	})
	return &Writer{pw: pw, err: err, opts: *opts}
}
func (w *Writer) WriteQuad(ctx context.Context, q quad.Quad) error {
	if w.err != nil {
		return w.err
	} else if !q.IsValid() {
		return quad.ErrInvalid
	}
	if !w.opts.Full {
		if q.Subject == w.s {
			q.Subject = nil
		} else {
			w.s = q.Subject
		}
		if q.Predicate == w.p {
			q.Predicate = nil
		} else {
			w.p = q.Predicate
		}
		if q.Object == w.o {
			q.Object = nil
		} else {
			w.o = q.Object
		}
	}
	var m proto.Message
	if w.opts.Strict {
		m, w.err = makeStrictQuad(q)
		if w.err != nil {
			return w.err
		}
	} else {
		m = makeWireQuad(q)
	}
	var n int
	n, w.err = w.pw.WriteMsg(m)
	if n > w.max {
		w.max = n
	}
	return w.err
}

func (w *Writer) WriteQuads(ctx context.Context, buf []quad.Quad) (int, error) {
	for i, q := range buf {
		if err := w.WriteQuad(ctx, q); err != nil {
			return i, err
		}
	}
	return len(buf), nil
}

// MaxSize returns a maximal message size written.
func (w *Writer) MaxSize() int {
	return w.max
}
func (w *Writer) SetCloser(c io.Closer) {
	w.cl = c
}
func (w *Writer) Close() error {
	if w.cl != nil {
		return w.cl.Close()
	}
	return nil
}

type Reader struct {
	pr      pio.Reader
	err     error
	opts    Options
	s, p, o quad.Value
	cl      io.Closer
}

func (r *Reader) SetCloser(c io.Closer) {
	r.cl = c
}

var _ quad.Skipper = (*Reader)(nil)

// NewReader creates protobuf quads decoder.
//
// MaxSize argument limits maximal size of the buffer used to read quads.
func NewReader(r io.Reader, maxSize int) *Reader {
	if maxSize <= 0 {
		maxSize = DefaultMaxSize
	}
	qr := &Reader{}
	buf := make([]byte, 8)
	if _, err := io.ReadFull(r, buf); err != nil {
		qr.err = err
		return qr
	} else if bytes.Compare(magic[:], buf[:4]) != 0 {
		qr.err = fmt.Errorf("not a pquads file")
		return qr
	}
	vers := binary.LittleEndian.Uint32(buf[4:])
	if vers != currentVersion {
		qr.err = fmt.Errorf("unsupported pquads version: %d", vers)
		return qr
	}

	qr.pr = pio.NewReader(r, maxSize)
	var h Header
	if err := qr.pr.ReadMsg(&h); err != nil {
		qr.err = err
	}
	qr.opts = Options{
		Full:   h.Full,
		Strict: !h.NotStrict,
	}
	return qr
}
func (r *Reader) ReadQuad(ctx context.Context) (quad.Quad, error) {
	if r.err != nil {
		return quad.Quad{}, r.err
	}
	var q quad.Quad
	if r.opts.Strict {
		var pq StrictQuad
		if r.err = r.pr.ReadMsg(&pq); r.err != nil {
			return quad.Quad{}, r.err
		}
		q = pq.ToNative()
	} else {
		var pq WireQuad
		if r.err = r.pr.ReadMsg(&pq); r.err != nil {
			return quad.Quad{}, r.err
		}
		q = pq.ToNative()
	}
	if q.Subject == nil {
		q.Subject = r.s
	} else {
		r.s = q.Subject
	}
	if q.Predicate == nil {
		q.Predicate = r.p
	} else {
		r.p = q.Predicate
	}
	if q.Object == nil {
		q.Object = r.o
	} else {
		r.o = q.Object
	}
	return q, nil
}
func (r *Reader) SkipQuad(ctx context.Context) error {
	if !r.opts.Full {
		// TODO(dennwc): read pb fields as bytes and unmarshal them only if ReadQuad is called
		_, err := r.ReadQuad(ctx)
		return err
	}
	r.err = r.pr.SkipMsg()
	return r.err
}
func (r *Reader) Close() error {
	if r.cl != nil {
		return r.cl.Close()
	}
	return nil
}
