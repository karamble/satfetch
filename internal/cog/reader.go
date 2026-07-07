// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package cog

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"sync"
)

// maxTileBytes bounds a single compressed tile allocation.
const maxTileBytes = 256 << 20

// Raster is a decoded pixel window. Exactly one of U8, U16 or F32 is set,
// according to Bits, holding W*H*SPP samples in row-major chunky order.
type Raster struct {
	W, H, SPP, Bits int
	U8              []uint8
	U16             []uint16
	F32             []float32
}

// Decimation returns the resolution reduction factor of a level relative to
// full resolution.
func (f *File) Decimation(level int) int {
	d := math.Round(float64(f.IFDs[0].Width) / float64(f.IFDs[level].Width))
	if d < 1 {
		d = 1
	}
	return int(d)
}

// PickLevel returns the finest level whose output for a window of winW x
// winH full-resolution pixels stays within maxPx per side. maxPx <= 0 keeps
// full resolution.
func (f *File) PickLevel(maxPx, winW, winH int) int {
	if maxPx <= 0 {
		return 0
	}
	for i := range f.IFDs {
		d := f.Decimation(i)
		if (winW+d-1)/d <= maxPx && (winH+d-1)/d <= maxPx {
			return i
		}
	}
	return len(f.IFDs) - 1
}

// LevelGrid returns the georeferencing of a level.
func (f *File) LevelGrid(level int) Grid {
	g := f.Grid
	d := float64(f.Decimation(level))
	g.ScaleX *= d
	g.ScaleY *= d
	return g
}

// ReadWindow decodes the given pixel window of a level, fetching only the
// covering tiles, at most conc at a time.
func (f *File) ReadWindow(ctx context.Context, level, x0, y0, w, h, conc int) (*Raster, error) {
	if level < 0 || level >= len(f.IFDs) {
		return nil, fmt.Errorf("cog: no such level %d", level)
	}
	ifd := &f.IFDs[level]
	if x0 < 0 || y0 < 0 || w <= 0 || h <= 0 || x0+w > ifd.Width || y0+h > ifd.Height {
		return nil, fmt.Errorf("cog: window %d,%d %dx%d out of bounds for %dx%d",
			x0, y0, w, h, ifd.Width, ifd.Height)
	}
	ras := &Raster{W: w, H: h, SPP: ifd.SPP, Bits: ifd.Bits}
	switch ifd.Bits {
	case 8:
		ras.U8 = make([]uint8, w*h*ifd.SPP)
	case 16:
		ras.U16 = make([]uint16, w*h*ifd.SPP)
	case 32:
		ras.F32 = make([]float32, w*h*ifd.SPP)
	}

	type job struct{ tx, ty int }
	var jobs []job
	for ty := y0 / ifd.TileHeight; ty <= (y0+h-1)/ifd.TileHeight; ty++ {
		for tx := x0 / ifd.TileWidth; tx <= (x0+w-1)/ifd.TileWidth; tx++ {
			jobs = append(jobs, job{tx, ty})
		}
	}
	if conc <= 0 {
		conc = 4
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(j job) {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			if err := f.readTile(ctx, ifd, j.tx, j.ty, ras, x0, y0); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				mu.Unlock()
			}
		}(j)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return ras, nil
}

// readTile fetches, decodes and blits one tile's intersection with the
// window anchored at wx0,wy0.
func (f *File) readTile(ctx context.Context, ifd *IFD, tx, ty int, out *Raster, wx0, wy0 int) error {
	across := (ifd.Width + ifd.TileWidth - 1) / ifd.TileWidth
	idx := ty*across + tx
	rows := ifd.TileHeight
	if !ifd.Tiled {
		// The last strip holds only the remaining rows.
		if r := ifd.Height - ty*ifd.TileHeight; r < rows {
			rows = r
		}
	}
	bytesPer := ifd.Bits / 8
	rowBytes := ifd.TileWidth * ifd.SPP * bytesPer
	expected := rows * rowBytes

	var data []byte
	if ifd.Counts[idx] == 0 {
		data = make([]byte, expected)
	} else {
		if ifd.Counts[idx] > maxTileBytes {
			return fmt.Errorf("cog: tile %d,%d is unreasonably large (%d bytes)", tx, ty, ifd.Counts[idx])
		}
		comp := make([]byte, ifd.Counts[idx])
		n, err := f.src.ReadRange(ctx, ifd.Offsets[idx], comp)
		if err != nil {
			return fmt.Errorf("cog: tile %d,%d: %w", tx, ty, err)
		}
		if n < len(comp) {
			return fmt.Errorf("cog: tile %d,%d truncated", tx, ty)
		}
		switch ifd.Compression {
		case compressionNone:
			data = comp
		case compressionDeflate, compressionOldDeflate:
			zr, err := zlib.NewReader(bytes.NewReader(comp))
			if err != nil {
				return fmt.Errorf("cog: tile %d,%d: %w", tx, ty, err)
			}
			data = make([]byte, expected)
			if _, err := io.ReadFull(zr, data); err != nil {
				return fmt.Errorf("cog: tile %d,%d: short inflate: %w", tx, ty, err)
			}
			zr.Close()
		}
		if len(data) < expected {
			return fmt.Errorf("cog: tile %d,%d: have %d bytes, expected %d", tx, ty, len(data), expected)
		}
		if ifd.Predictor == 2 {
			undoPredictor(data, rows, ifd.TileWidth, ifd.SPP, ifd.Bits)
		}
	}

	tileX, tileY := tx*ifd.TileWidth, ty*ifd.TileHeight
	ix0 := max(wx0, tileX)
	iy0 := max(wy0, tileY)
	ix1 := min(wx0+out.W, tileX+ifd.TileWidth)
	iy1 := min(wy0+out.H, tileY+rows)
	if ix0 >= ix1 || iy0 >= iy1 {
		return nil
	}
	spp := ifd.SPP
	for y := iy0; y < iy1; y++ {
		srcOff := ((y-tileY)*ifd.TileWidth + (ix0 - tileX)) * spp
		dstOff := ((y-wy0)*out.W + (ix0 - wx0)) * spp
		count := (ix1 - ix0) * spp
		switch ifd.Bits {
		case 8:
			copy(out.U8[dstOff:dstOff+count], data[srcOff:srcOff+count])
		case 16:
			for i := 0; i < count; i++ {
				out.U16[dstOff+i] = binary.LittleEndian.Uint16(data[(srcOff+i)*2:])
			}
		case 32:
			for i := 0; i < count; i++ {
				out.F32[dstOff+i] = math.Float32frombits(binary.LittleEndian.Uint32(data[(srcOff+i)*4:]))
			}
		}
	}
	return nil
}
