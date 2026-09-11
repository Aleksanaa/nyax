// SPDX-License-Identifier: LGPL-2.1-or-later
//
// This file is part of Nyax. Nyax is based on Orivej Desh's go-nix
// (https://github.com/orivej/go-nix), which is released under the UNLICENSE,
// and its behaviour follows the Nix implementation
// (https://github.com/NixOS/nix); all Nix contributors are gratefully
// acknowledged. See the LICENSE and NOTICE files.

package nixhash

import (
	"encoding/binary"
	"io"

	"github.com/orivej/e"
)

var sinkPad = make([]byte, 8)

// Sink encodes writes as:
// - 8 bytes size
// - data
// - pad to 8 bytes with zeroes
type Sink interface {
	Begin(int64) error
	End() error
	io.Writer
	B([]byte)
	S(...string)
}

func NewSink(w io.Writer) Sink {
	return &sink{w: w, n: -1}
}

type sink struct {
	w io.Writer
	n int64
}

func (sink *sink) Begin(size int64) error {
	sink.n = size
	return binary.Write(sink.w, binary.LittleEndian, size)
}

func (sink *sink) End() error {
	_, err := sink.w.Write(sinkPad[:uint64(-sink.n)%8])
	sink.n = -1
	return err
}

func (sink *sink) Write(data []byte) (int, error) {
	atomic := sink.n == -1
	if atomic {
		err := sink.Begin(int64(len(data)))
		if err != nil {
			return 0, err
		}
	}
	n, err := sink.w.Write(data)
	if err != nil {
		return n, err
	}
	if atomic {
		err = sink.End()
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func (sink *sink) B(data []byte) {
	_, err := sink.Write(data)
	e.Exit(err)
}

func (sink *sink) S(ss ...string) {
	for _, s := range ss {
		sink.B([]byte(s))
	}
}
