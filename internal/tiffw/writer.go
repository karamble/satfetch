// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

// Package tiffw writes minimal little-endian classic GeoTIFF files: striped
// single-level products (true-color uint8 RGB, float32 rasters) and tiled
// multi-level fixtures used by tests.
package tiffw

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"sort"
)

// Geo carries the georeferencing written into a file. KeyDir and Ascii are
// copied verbatim from the source COG. Zero ScaleX omits all geo tags.
type Geo struct {
	ScaleX, ScaleY   float64
	OriginX, OriginY float64
	KeyDir           []uint16
	Ascii            string
	NoData           string
}

const (
	typeASCII  = 2
	typeShort  = 3
	typeLong   = 4
	typeDouble = 12
)

type tag struct {
	id    uint16
	typ   uint16
	count uint32
	data  []byte
}

func shortTag(id uint16, vals ...uint16) tag {
	b := make([]byte, 2*len(vals))
	for i, v := range vals {
		binary.LittleEndian.PutUint16(b[i*2:], v)
	}
	return tag{id: id, typ: typeShort, count: uint32(len(vals)), data: b}
}

func longTag(id uint16, vals ...uint32) tag {
	b := make([]byte, 4*len(vals))
	for i, v := range vals {
		binary.LittleEndian.PutUint32(b[i*4:], v)
	}
	return tag{id: id, typ: typeLong, count: uint32(len(vals)), data: b}
}

func doubleTag(id uint16, vals ...float64) tag {
	b := make([]byte, 8*len(vals))
	for i, v := range vals {
		binary.LittleEndian.PutUint64(b[i*8:], math.Float64bits(v))
	}
	return tag{id: id, typ: typeDouble, count: uint32(len(vals)), data: b}
}

func asciiTag(id uint16, s string) tag {
	b := append([]byte(s), 0)
	return tag{id: id, typ: typeASCII, count: uint32(len(b)), data: b}
}

// ifdSpec is one image in a file: its tags (except segment locations, which
// the assembler adds) and its compressed segment payloads (strips or tiles).
type ifdSpec struct {
	tags       []tag
	segOffsets uint16 // 273 or 324
	segCounts  uint16 // 279 or 325
	segs       [][]byte
}

// writeFile lays out and emits a classic little-endian TIFF: header, all
// IFDs with their overflow tag data, then all segment data.
func writeFile(w io.Writer, ifds []ifdSpec) error {
	type placed struct {
		tags    []tag
		off     int
		valOff  []int // per tag: offset of overflow data, 0 = inline
		segBase []int // per segment: absolute offset
	}
	pos := 8
	placedIFDs := make([]placed, len(ifds))

	// First pass: assign IFD and overflow offsets. Segment offset tags are
	// sized now and patched later.
	for i := range ifds {
		spec := &ifds[i]
		segOff := make([]uint32, len(spec.segs))
		segCnt := make([]uint32, len(spec.segs))
		for j, s := range spec.segs {
			segCnt[j] = uint32(len(s))
		}
		tags := append([]tag{}, spec.tags...)
		tags = append(tags, longTag(spec.segOffsets, segOff...), longTag(spec.segCounts, segCnt...))
		sort.Slice(tags, func(a, b int) bool { return tags[a].id < tags[b].id })

		p := placed{tags: tags, off: pos, valOff: make([]int, len(tags))}
		pos += 2 + len(tags)*12 + 4
		for t := range tags {
			if len(tags[t].data) > 4 {
				if pos%2 == 1 {
					pos++
				}
				p.valOff[t] = pos
				pos += len(tags[t].data)
			}
		}
		placedIFDs[i] = p
	}
	// Second pass: assign segment data offsets.
	for i := range ifds {
		p := &placedIFDs[i]
		p.segBase = make([]int, len(ifds[i].segs))
		for j, s := range ifds[i].segs {
			if pos%2 == 1 {
				pos++
			}
			p.segBase[j] = pos
			pos += len(s)
		}
		// Patch the segment offsets tag payload.
		for t := range p.tags {
			if p.tags[t].id == ifds[i].segOffsets {
				for j, off := range p.segBase {
					binary.LittleEndian.PutUint32(p.tags[t].data[j*4:], uint32(off))
				}
			}
		}
	}

	buf := make([]byte, pos)
	buf[0], buf[1] = 'I', 'I'
	binary.LittleEndian.PutUint16(buf[2:], 42)
	binary.LittleEndian.PutUint32(buf[4:], uint32(placedIFDs[0].off))
	for i := range placedIFDs {
		p := &placedIFDs[i]
		binary.LittleEndian.PutUint16(buf[p.off:], uint16(len(p.tags)))
		for t, tg := range p.tags {
			eo := p.off + 2 + t*12
			binary.LittleEndian.PutUint16(buf[eo:], tg.id)
			binary.LittleEndian.PutUint16(buf[eo+2:], tg.typ)
			binary.LittleEndian.PutUint32(buf[eo+4:], tg.count)
			if len(tg.data) <= 4 {
				copy(buf[eo+8:eo+12], tg.data)
			} else {
				binary.LittleEndian.PutUint32(buf[eo+8:], uint32(p.valOff[t]))
				copy(buf[p.valOff[t]:], tg.data)
			}
		}
		next := 0
		if i+1 < len(placedIFDs) {
			next = placedIFDs[i+1].off
		}
		binary.LittleEndian.PutUint32(buf[p.off+2+len(p.tags)*12:], uint32(next))
		for j, s := range ifds[i].segs {
			copy(buf[p.segBase[j]:], s)
		}
	}
	_, err := w.Write(buf)
	return err
}

func geoTags(g Geo) []tag {
	var tags []tag
	if g.ScaleX > 0 {
		tags = append(tags,
			doubleTag(33550, g.ScaleX, g.ScaleY, 0),
			doubleTag(33922, 0, 0, 0, g.OriginX, g.OriginY, 0))
	}
	if len(g.KeyDir) > 0 {
		tags = append(tags, shortTag(34735, g.KeyDir...))
	}
	if g.Ascii != "" {
		tags = append(tags, asciiTag(34737, g.Ascii))
	}
	if g.NoData != "" {
		tags = append(tags, asciiTag(42113, g.NoData))
	}
	return tags
}

func deflate(p []byte) []byte {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	zw.Write(p)
	zw.Close()
	return buf.Bytes()
}

// stripSpec builds a striped deflate-compressed single image from raw
// little-endian pixel bytes.
func stripSpec(width, height, spp, bits, sampleFormat, photometric int, raw []byte, g Geo) (ifdSpec, error) {
	bytesPer := bits / 8
	rowBytes := width * spp * bytesPer
	if len(raw) != rowBytes*height {
		return ifdSpec{}, fmt.Errorf("tiffw: have %d pixel bytes, expected %d", len(raw), rowBytes*height)
	}
	rps := (1 << 20) / rowBytes
	if rps < 1 {
		rps = 1
	}
	if rps > height {
		rps = height
	}
	nStrips := (height + rps - 1) / rps
	segs := make([][]byte, nStrips)
	for i := 0; i < nStrips; i++ {
		r0 := i * rps
		r1 := min(r0+rps, height)
		segs[i] = deflate(raw[r0*rowBytes : r1*rowBytes])
	}
	bps := make([]uint16, spp)
	sf := make([]uint16, spp)
	for i := 0; i < spp; i++ {
		bps[i] = uint16(bits)
		sf[i] = uint16(sampleFormat)
	}
	tags := []tag{
		longTag(256, uint32(width)),
		longTag(257, uint32(height)),
		shortTag(258, bps...),
		shortTag(259, 8),
		shortTag(262, uint16(photometric)),
		shortTag(277, uint16(spp)),
		longTag(278, uint32(rps)),
		shortTag(284, 1),
		shortTag(339, sf...),
	}
	tags = append(tags, geoTags(g)...)
	return ifdSpec{tags: tags, segOffsets: 273, segCounts: 279, segs: segs}, nil
}

// WriteRGB8 writes a striped deflate RGB GeoTIFF from chunky 3-sample
// uint8 pixels.
func WriteRGB8(w io.Writer, width, height int, pix []uint8, g Geo) error {
	spec, err := stripSpec(width, height, 3, 8, 1, 2, pix, g)
	if err != nil {
		return err
	}
	return writeFile(w, []ifdSpec{spec})
}

// WriteFloat32 writes a striped deflate single-band float32 GeoTIFF.
func WriteFloat32(w io.Writer, width, height int, vals []float32, g Geo) error {
	if len(vals) != width*height {
		return fmt.Errorf("tiffw: have %d values, expected %d", len(vals), width*height)
	}
	raw := make([]byte, 4*len(vals))
	for i, v := range vals {
		binary.LittleEndian.PutUint32(raw[i*4:], math.Float32bits(v))
	}
	spec, err := stripSpec(width, height, 1, 32, 3, 1, raw, g)
	if err != nil {
		return err
	}
	return writeFile(w, []ifdSpec{spec})
}
