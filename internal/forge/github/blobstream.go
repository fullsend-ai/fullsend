package github

import (
	"encoding/base64"
	"io"
)

const (
	blobJSONPrefix = `{"content":"`
	blobJSONSuffix = `","encoding":"base64"}`
)

func blobJSONLength(size int64) int64 {
	return int64(len(blobJSONPrefix)+len(blobJSONSuffix)) + ((size+2)/3)*4
}

// blobJSONReadCloser streams a Git Blobs API JSON body:
//
//	{"content":"<base64>","encoding":"base64"}
//
// The source is encoded in 3-byte chunks so the raw file is never held in
// memory alongside the base64 payload.
type blobJSONReadCloser struct {
	src    io.ReadCloser
	prefix []byte
	suffix []byte
	raw    [3]byte
	rawN   int
	enc    [4]byte
	encOff int
	encN   int
	phase  int // 0 prefix, 1 body, 2 suffix, 3 done
	eof    bool
}

func newBlobJSONReadCloser(src io.ReadCloser) *blobJSONReadCloser {
	return &blobJSONReadCloser{
		src:    src,
		prefix: []byte(blobJSONPrefix),
		suffix: []byte(blobJSONSuffix),
	}
}

func (r *blobJSONReadCloser) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n := 0
	for n < len(p) {
		switch r.phase {
		case 0:
			if len(r.prefix) == 0 {
				r.phase = 1
				continue
			}
			c := copy(p[n:], r.prefix)
			r.prefix = r.prefix[c:]
			n += c
		case 1:
			if r.encOff < r.encN {
				c := copy(p[n:], r.enc[r.encOff:r.encN])
				r.encOff += c
				n += c
				continue
			}
			if r.eof {
				r.phase = 2
				continue
			}
			for r.rawN < 3 && !r.eof {
				nn, err := r.src.Read(r.raw[r.rawN:])
				r.rawN += nn
				if err == io.EOF {
					r.eof = true
					break
				}
				if err != nil {
					if n > 0 {
						return n, err
					}
					return 0, err
				}
				if nn == 0 {
					break
				}
			}
			if r.rawN == 0 {
				if r.eof {
					r.phase = 2
					continue
				}
				return n, nil
			}
			if r.rawN < 3 && !r.eof {
				return n, nil
			}
			r.encN = base64.StdEncoding.EncodedLen(r.rawN)
			base64.StdEncoding.Encode(r.enc[:], r.raw[:r.rawN])
			r.encOff = 0
			r.rawN = 0
		case 2:
			if len(r.suffix) == 0 {
				r.phase = 3
				continue
			}
			c := copy(p[n:], r.suffix)
			r.suffix = r.suffix[c:]
			n += c
		default:
			if n == 0 {
				return 0, io.EOF
			}
			return n, nil
		}
	}
	return n, nil
}

func (r *blobJSONReadCloser) Close() error {
	return r.src.Close()
}
