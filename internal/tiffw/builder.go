// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package tiffw

import (
	"encoding/binary"
	"fmt"
	"io"
)

// COGSpec describes a synthetic tiled COG used as a test fixture: a base
// image plus nearest-neighbor decimated overview levels, deflate-compressed
// with TIFF predictor 2, mirroring the layout of the sentinel-cogs files.
type COGSpec struct {
	Width, Height int
	TileSize      int
	SPP           int // 1, 3 or 4
	Bits          int // 8 or 16
	Levels        []int
	Geo           Geo
	Pix8          []uint8
	Pix16         []uint16
	SparseTiles   map[[2]int]bool // level 0 tiles emitted with byte count 0
}

// WriteCOG writes the fixture file.
func WriteCOG(w io.Writer, s COGSpec) error {
	if s.TileSize <= 0 {
		return fmt.Errorf("tiffw: bad tile size")
	}
	if len(s.Levels) == 0 || s.Levels[0] != 1 {
		return fmt.Errorf("tiffw: levels must start with decimation 1")
	}
	bytesPer := s.Bits / 8
	var base []byte
	switch s.Bits {
	case 8:
		if len(s.Pix8) != s.Width*s.Height*s.SPP {
			return fmt.Errorf("tiffw: have %d pixels, expected %d", len(s.Pix8), s.Width*s.Height*s.SPP)
		}
		base = s.Pix8
	case 16:
		if len(s.Pix16) != s.Width*s.Height*s.SPP {
			return fmt.Errorf("tiffw: have %d pixels, expected %d", len(s.Pix16), s.Width*s.Height*s.SPP)
		}
		base = make([]byte, 2*len(s.Pix16))
		for i, v := range s.Pix16 {
			binary.LittleEndian.PutUint16(base[i*2:], v)
		}
	default:
		return fmt.Errorf("tiffw: unsupported bit depth %d", s.Bits)
	}

	var ifds []ifdSpec
	for li, dec := range s.Levels {
		lw := (s.Width + dec - 1) / dec
		lh := (s.Height + dec - 1) / dec
		lraw := decimate(base, s.Width, s.Height, s.SPP, bytesPer, dec)

		across := (lw + s.TileSize - 1) / s.TileSize
		down := (lh + s.TileSize - 1) / s.TileSize
		segs := make([][]byte, 0, across*down)
		for ty := 0; ty < down; ty++ {
			for tx := 0; tx < across; tx++ {
				if li == 0 && s.SparseTiles[[2]int{tx, ty}] {
					segs = append(segs, nil)
					continue
				}
				tile := extractTile(lraw, lw, lh, s.SPP, bytesPer, tx, ty, s.TileSize)
				applyPredictor2(tile, s.TileSize, s.TileSize, s.SPP, s.Bits)
				segs = append(segs, deflate(tile))
			}
		}
		bps := make([]uint16, s.SPP)
		sf := make([]uint16, s.SPP)
		for i := range bps {
			bps[i] = uint16(s.Bits)
			sf[i] = 1
		}
		photo := 1
		if s.SPP >= 3 {
			photo = 2
		}
		tags := []tag{
			longTag(256, uint32(lw)),
			longTag(257, uint32(lh)),
			shortTag(258, bps...),
			shortTag(259, 8),
			shortTag(262, uint16(photo)),
			shortTag(277, uint16(s.SPP)),
			shortTag(284, 1),
			shortTag(317, 2),
			shortTag(322, uint16(s.TileSize)),
			shortTag(323, uint16(s.TileSize)),
			shortTag(339, sf...),
		}
		if li == 0 {
			tags = append(tags, geoTags(s.Geo)...)
		} else {
			tags = append(tags, longTag(254, 1))
		}
		ifds = append(ifds, ifdSpec{tags: tags, segOffsets: 324, segCounts: 325, segs: segs})
	}
	return writeFile(w, ifds)
}

// decimate reduces a chunky raster by nearest-neighbor sampling.
func decimate(raw []byte, w, h, spp, bytesPer, dec int) []byte {
	if dec == 1 {
		return raw
	}
	lw := (w + dec - 1) / dec
	lh := (h + dec - 1) / dec
	px := spp * bytesPer
	out := make([]byte, lw*lh*px)
	for y := 0; y < lh; y++ {
		sy := min(y*dec, h-1)
		for x := 0; x < lw; x++ {
			sx := min(x*dec, w-1)
			copy(out[(y*lw+x)*px:(y*lw+x+1)*px], raw[(sy*w+sx)*px:(sy*w+sx+1)*px])
		}
	}
	return out
}

// extractTile copies one tile out of a chunky raster, zero-padding past the
// right and bottom edges the way GDAL does.
func extractTile(raw []byte, w, h, spp, bytesPer, tx, ty, tile int) []byte {
	px := spp * bytesPer
	out := make([]byte, tile*tile*px)
	for y := 0; y < tile; y++ {
		sy := ty*tile + y
		if sy >= h {
			break
		}
		cols := min(tile, w-tx*tile)
		if cols <= 0 {
			break
		}
		copy(out[y*tile*px:(y*tile+cols)*px], raw[(sy*w+tx*tile)*px:(sy*w+tx*tile+cols)*px])
	}
	return out
}

// applyPredictor2 applies TIFF horizontal differencing in place.
func applyPredictor2(data []byte, rows, width, spp, bits int) {
	switch bits {
	case 8:
		rowBytes := width * spp
		for r := 0; r < rows; r++ {
			row := data[r*rowBytes : (r+1)*rowBytes]
			for i := len(row) - 1; i >= spp; i-- {
				row[i] -= row[i-spp]
			}
		}
	case 16:
		rowVals := width * spp
		for r := 0; r < rows; r++ {
			row := data[r*rowVals*2 : (r+1)*rowVals*2]
			for i := rowVals - 1; i >= spp; i-- {
				v := binary.LittleEndian.Uint16(row[i*2:]) - binary.LittleEndian.Uint16(row[(i-spp)*2:])
				binary.LittleEndian.PutUint16(row[i*2:], v)
			}
		}
	}
}
