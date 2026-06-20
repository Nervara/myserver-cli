package main

import (
	"fmt"
	"io"
	"os"
)

type uploadProgressReader struct {
	r     io.Reader
	out   io.Writer
	label string
	every int64
	read  int64
	last  int64
	done  bool
}

func newUploadProgressReader(r io.Reader, label string) *uploadProgressReader {
	return &uploadProgressReader{
		r:     r,
		out:   os.Stderr,
		label: label,
		every: 1 << 20,
	}
}

func (p *uploadProgressReader) Read(buf []byte) (int, error) {
	n, err := p.r.Read(buf)
	if n > 0 {
		p.read += int64(n)
		if p.read-p.last >= p.every {
			p.print(false)
			p.last = p.read
		}
	}
	return n, err
}

func (p *uploadProgressReader) Finish() {
	if p.done {
		return
	}
	p.print(true)
	p.done = true
}

func (p *uploadProgressReader) print(final bool) {
	if p.out == nil {
		return
	}
	suffix := ""
	if final {
		suffix = "\n"
	}
	fmt.Fprintf(p.out, "\r  %s %.1fMB%s", p.label, float64(p.read)/(1<<20), suffix)
}
