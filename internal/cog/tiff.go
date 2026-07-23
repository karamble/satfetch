// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package cog

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
)

const (
	tagNewSubfileType  = 254
	tagImageWidth      = 256
	tagImageLength     = 257
	tagBitsPerSample   = 258
	tagCompression     = 259
	tagPhotometric     = 262
	tagStripOffsets    = 273
	tagSamplesPerPixel = 277
	tagRowsPerStrip    = 278
	tagStripByteCounts = 279
	tagPlanarConfig    = 284
	tagPredictor       = 317
	tagTileWidth       = 322
	tagTileLength      = 323
	tagTileOffsets     = 324
	tagTileByteCounts  = 325
	tagSampleFormat    = 339
	tagModelPixelScale = 33550
	tagModelTiepoint   = 33922
	tagGeoKeyDirectory = 34735
	tagGeoAsciiParams  = 34737
	tagGDALNoData      = 42113
)

const (
	compressionNone       = 1
	compressionDeflate    = 8
	compressionOldDeflate = 32946
)

// geoKeyProjectedCSType is the GeoTIFF key holding the projected CRS code.
const geoKeyProjectedCSType = 3072

type entry struct {
	typ   uint16
	count uint32
	raw   [4]byte
}

type ifdRaw map[uint16]entry

// IFD describes one resolution level.
type IFD struct {
	Width, Height         int
	TileWidth, TileHeight int
	Tiled                 bool
	Offsets, Counts       []int64
	Compression           int
	Predictor             int
	Bits                  int
	SPP                   int
	SampleFormat          int
	Subfile               int
}

// Grid georeferences a raster level: the model coordinate of the upper-left
// corner of pixel (0,0) and the pixel size in model units.
type Grid struct {
	EPSG             int
	OriginX, OriginY float64
	ScaleX, ScaleY   float64
}

// Window converts a projected bounding box into a clamped pixel window on a
// raster of the given dimensions. ok is false when the box misses the raster
// entirely or the grid is not georeferenced.
func (g Grid) Window(w, h int, minX, minY, maxX, maxY float64) (x0, y0, ww, wh int, ok bool) {
	if g.ScaleX <= 0 || g.ScaleY <= 0 {
		return 0, 0, 0, 0, false
	}
	c0 := int(math.Floor((minX - g.OriginX) / g.ScaleX))
	c1 := int(math.Ceil((maxX - g.OriginX) / g.ScaleX))
	r0 := int(math.Floor((g.OriginY - maxY) / g.ScaleY))
	r1 := int(math.Ceil((g.OriginY - minY) / g.ScaleY))
	c0 = max(c0, 0)
	r0 = max(r0, 0)
	c1 = min(c1, w)
	r1 = min(r1, h)
	if c0 >= c1 || r0 >= r1 {
		return 0, 0, 0, 0, false
	}
	return c0, r0, c1 - c0, r1 - r0, true
}

// File is an opened COG with its parsed resolution levels. IFDs are ordered
// from full resolution to the coarsest overview.
type File struct {
	src Source
	hdr []byte
	eof bool

	IFDs     []IFD
	Grid     Grid
	KeyDir   []uint16 // raw GeoKeyDirectory, reusable by writers
	GeoAscii string
	NoData   string
}

const (
	hdrChunk = 64 << 10
	hdrMax   = 4 << 20
)

// Open fetches and parses the TIFF header and all IFDs.
func Open(ctx context.Context, src Source) (*File, error) {
	f := &File{src: src}
	if err := f.ensure(ctx, 8); err != nil {
		return nil, err
	}
	switch {
	case f.hdr[0] == 'I' && f.hdr[1] == 'I':
	case f.hdr[0] == 'M' && f.hdr[1] == 'M':
		return nil, fmt.Errorf("cog: big-endian TIFF not supported")
	default:
		return nil, fmt.Errorf("cog: not a TIFF")
	}
	magic := binary.LittleEndian.Uint16(f.hdr[2:4])
	if magic == 43 {
		return nil, fmt.Errorf("cog: BigTIFF not supported")
	}
	if magic != 42 {
		return nil, fmt.Errorf("cog: bad TIFF magic %d", magic)
	}
	off := int64(binary.LittleEndian.Uint32(f.hdr[4:8]))
	for off != 0 {
		next, err := f.parseIFD(ctx, off)
		if err != nil {
			return nil, err
		}
		off = next
		if len(f.IFDs) > 32 {
			return nil, fmt.Errorf("cog: too many IFDs")
		}
	}
	if len(f.IFDs) == 0 {
		return nil, fmt.Errorf("cog: no images")
	}
	sort.SliceStable(f.IFDs, func(i, j int) bool { return f.IFDs[i].Width > f.IFDs[j].Width })
	return f, nil
}

// ensure grows the header buffer to hold at least n bytes.
func (f *File) ensure(ctx context.Context, n int) error {
	if n <= len(f.hdr) {
		return nil
	}
	if f.eof {
		return fmt.Errorf("cog: header reference beyond end of file")
	}
	if n > hdrMax {
		return fmt.Errorf("cog: header exceeds %d bytes", hdrMax)
	}
	want := (n + hdrChunk - 1) / hdrChunk * hdrChunk
	buf := make([]byte, want-len(f.hdr))
	got, err := f.src.ReadRange(ctx, int64(len(f.hdr)), buf)
	if err != nil && err != io.EOF {
		return err
	}
	f.hdr = append(f.hdr, buf[:got]...)
	if got < len(buf) {
		f.eof = true
	}
	if n > len(f.hdr) {
		return fmt.Errorf("cog: truncated header")
	}
	return nil
}

func (f *File) parseIFD(ctx context.Context, off int64) (int64, error) {
	if err := f.ensure(ctx, int(off)+2); err != nil {
		return 0, err
	}
	n := int(binary.LittleEndian.Uint16(f.hdr[off : off+2]))
	end := int(off) + 2 + n*12 + 4
	if err := f.ensure(ctx, end); err != nil {
		return 0, err
	}
	raw := ifdRaw{}
	for i := 0; i < n; i++ {
		eo := int(off) + 2 + i*12
		tag := binary.LittleEndian.Uint16(f.hdr[eo:])
		e := entry{
			typ:   binary.LittleEndian.Uint16(f.hdr[eo+2:]),
			count: binary.LittleEndian.Uint32(f.hdr[eo+4:]),
		}
		copy(e.raw[:], f.hdr[eo+8:eo+12])
		raw[tag] = e
	}
	next := int64(binary.LittleEndian.Uint32(f.hdr[end-4 : end]))
	ifd, err := f.buildIFD(ctx, raw)
	if err != nil {
		return 0, err
	}
	f.IFDs = append(f.IFDs, ifd)
	if len(f.IFDs) == 1 {
		if err := f.parseGeo(ctx, raw); err != nil {
			return 0, err
		}
	}
	return next, nil
}

func typeSize(t uint16) int {
	switch t {
	case 1, 2, 6, 7:
		return 1
	case 3, 8:
		return 2
	case 4, 9, 11:
		return 4
	case 5, 10, 12:
		return 8
	}
	return 0
}

// tagData returns the value bytes of an entry, following the offset
// indirection when the value does not fit inline.
func (f *File) tagData(ctx context.Context, e entry) ([]byte, error) {
	sz := typeSize(e.typ) * int(e.count)
	if sz == 0 {
		return nil, fmt.Errorf("cog: unsupported tag type %d", e.typ)
	}
	if sz <= 4 {
		return e.raw[:sz], nil
	}
	off := int(binary.LittleEndian.Uint32(e.raw[:]))
	if err := f.ensure(ctx, off+sz); err != nil {
		return nil, err
	}
	return f.hdr[off : off+sz], nil
}

func (f *File) tagUints(ctx context.Context, e entry) ([]int64, error) {
	data, err := f.tagData(ctx, e)
	if err != nil {
		return nil, err
	}
	out := make([]int64, e.count)
	for i := range out {
		switch e.typ {
		case 3:
			out[i] = int64(binary.LittleEndian.Uint16(data[i*2:]))
		case 4:
			out[i] = int64(binary.LittleEndian.Uint32(data[i*4:]))
		default:
			return nil, fmt.Errorf("cog: tag type %d is not an unsigned integer", e.typ)
		}
	}
	return out, nil
}

func (f *File) tagDoubles(ctx context.Context, e entry) ([]float64, error) {
	if e.typ != 12 {
		return nil, fmt.Errorf("cog: tag type %d is not a double", e.typ)
	}
	data, err := f.tagData(ctx, e)
	if err != nil {
		return nil, err
	}
	out := make([]float64, e.count)
	for i := range out {
		out[i] = math.Float64frombits(binary.LittleEndian.Uint64(data[i*8:]))
	}
	return out, nil
}

func (f *File) tagString(ctx context.Context, e entry) (string, error) {
	if e.typ != 2 {
		return "", fmt.Errorf("cog: tag type %d is not ASCII", e.typ)
	}
	data, err := f.tagData(ctx, e)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(data), "\x00"), nil
}

func (f *File) uintTag(ctx context.Context, raw ifdRaw, tag uint16, def int64) (int64, error) {
	e, ok := raw[tag]
	if !ok {
		return def, nil
	}
	v, err := f.tagUints(ctx, e)
	if err != nil {
		return 0, err
	}
	if len(v) == 0 {
		return def, nil
	}
	return v[0], nil
}

func (f *File) buildIFD(ctx context.Context, raw ifdRaw) (IFD, error) {
	var ifd IFD
	var err error
	geti := func(tag uint16, def int64) int64 {
		if err != nil {
			return 0
		}
		var v int64
		v, err = f.uintTag(ctx, raw, tag, def)
		return v
	}
	ifd.Width = int(geti(tagImageWidth, 0))
	ifd.Height = int(geti(tagImageLength, 0))
	ifd.Bits = int(geti(tagBitsPerSample, 1))
	ifd.Compression = int(geti(tagCompression, compressionNone))
	ifd.SPP = int(geti(tagSamplesPerPixel, 1))
	ifd.Predictor = int(geti(tagPredictor, 1))
	ifd.SampleFormat = int(geti(tagSampleFormat, 1))
	ifd.Subfile = int(geti(tagNewSubfileType, 0))
	planar := int(geti(tagPlanarConfig, 1))
	if err != nil {
		return ifd, err
	}
	if ifd.Width <= 0 || ifd.Height <= 0 {
		return ifd, fmt.Errorf("cog: bad image dimensions %dx%d", ifd.Width, ifd.Height)
	}
	if _, ok := raw[tagTileOffsets]; ok {
		ifd.Tiled = true
		ifd.TileWidth = int(geti(tagTileWidth, 0))
		ifd.TileHeight = int(geti(tagTileLength, 0))
		if err == nil {
			ifd.Offsets, err = f.tagUints(ctx, raw[tagTileOffsets])
		}
		if err == nil {
			if e, ok := raw[tagTileByteCounts]; ok {
				ifd.Counts, err = f.tagUints(ctx, e)
			} else {
				err = fmt.Errorf("cog: missing TileByteCounts")
			}
		}
	} else if _, ok := raw[tagStripOffsets]; ok {
		ifd.TileWidth = ifd.Width
		rps := geti(tagRowsPerStrip, int64(ifd.Height))
		if rps <= 0 || rps > int64(ifd.Height) {
			rps = int64(ifd.Height)
		}
		ifd.TileHeight = int(rps)
		if err == nil {
			ifd.Offsets, err = f.tagUints(ctx, raw[tagStripOffsets])
		}
		if err == nil {
			if e, ok := raw[tagStripByteCounts]; ok {
				ifd.Counts, err = f.tagUints(ctx, e)
			} else {
				err = fmt.Errorf("cog: missing StripByteCounts")
			}
		}
	} else {
		return ifd, fmt.Errorf("cog: image has neither tiles nor strips")
	}
	if err != nil {
		return ifd, err
	}

	switch ifd.Compression {
	case compressionNone, compressionDeflate, compressionOldDeflate:
	default:
		return ifd, fmt.Errorf("cog: unsupported compression %d", ifd.Compression)
	}
	if planar != 1 {
		return ifd, fmt.Errorf("cog: unsupported planar configuration %d", planar)
	}
	switch ifd.Bits {
	case 8, 16:
		if ifd.SampleFormat != 1 {
			return ifd, fmt.Errorf("cog: unsupported sample format %d for %d-bit samples", ifd.SampleFormat, ifd.Bits)
		}
	case 32:
		if ifd.SampleFormat != 3 {
			return ifd, fmt.Errorf("cog: unsupported sample format %d for 32-bit samples", ifd.SampleFormat)
		}
	default:
		return ifd, fmt.Errorf("cog: unsupported bit depth %d", ifd.Bits)
	}
	if ifd.Predictor != 1 && (ifd.Predictor != 2 || ifd.Bits == 32) {
		return ifd, fmt.Errorf("cog: unsupported predictor %d", ifd.Predictor)
	}
	// 4 samples covers RGB+NIR imagery such as NAIP; readers take the
	// leading bands and ignore the rest.
	if ifd.SPP != 1 && ifd.SPP != 3 && ifd.SPP != 4 {
		return ifd, fmt.Errorf("cog: unsupported samples per pixel %d", ifd.SPP)
	}
	if ifd.TileWidth <= 0 || ifd.TileHeight <= 0 || ifd.TileWidth > 1<<14 || ifd.TileHeight > 1<<14 {
		return ifd, fmt.Errorf("cog: bad tile dimensions %dx%d", ifd.TileWidth, ifd.TileHeight)
	}
	across := (ifd.Width + ifd.TileWidth - 1) / ifd.TileWidth
	down := (ifd.Height + ifd.TileHeight - 1) / ifd.TileHeight
	if len(ifd.Offsets) != across*down || len(ifd.Counts) != across*down {
		return ifd, fmt.Errorf("cog: expected %d tiles, have %d offsets and %d counts",
			across*down, len(ifd.Offsets), len(ifd.Counts))
	}
	return ifd, nil
}

func (f *File) parseGeo(ctx context.Context, raw ifdRaw) error {
	if e, ok := raw[tagModelPixelScale]; ok {
		d, err := f.tagDoubles(ctx, e)
		if err != nil {
			return err
		}
		if len(d) >= 2 {
			f.Grid.ScaleX, f.Grid.ScaleY = d[0], d[1]
		}
	}
	if e, ok := raw[tagModelTiepoint]; ok {
		d, err := f.tagDoubles(ctx, e)
		if err != nil {
			return err
		}
		if len(d) >= 6 {
			f.Grid.OriginX = d[3] - d[0]*f.Grid.ScaleX
			f.Grid.OriginY = d[4] + d[1]*f.Grid.ScaleY
		}
	}
	if e, ok := raw[tagGeoKeyDirectory]; ok {
		v, err := f.tagUints(ctx, e)
		if err != nil {
			return err
		}
		f.KeyDir = make([]uint16, len(v))
		for i, x := range v {
			f.KeyDir[i] = uint16(x)
		}
		for i := 4; i+3 < len(f.KeyDir); i += 4 {
			if f.KeyDir[i] == geoKeyProjectedCSType && f.KeyDir[i+1] == 0 {
				f.Grid.EPSG = int(f.KeyDir[i+3])
			}
		}
	}
	if e, ok := raw[tagGeoAsciiParams]; ok {
		s, err := f.tagString(ctx, e)
		if err != nil {
			return err
		}
		f.GeoAscii = s
	}
	if e, ok := raw[tagGDALNoData]; ok {
		s, err := f.tagString(ctx, e)
		if err != nil {
			return err
		}
		f.NoData = strings.TrimSpace(s)
	}
	return nil
}
