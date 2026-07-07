// Copyright (c) 2026 The Decred developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package cog

import "encoding/binary"

// undoPredictor reverses TIFF predictor 2 (horizontal differencing) in
// place. data holds rows of width*spp samples, little-endian for 16-bit.
func undoPredictor(data []byte, rows, width, spp, bits int) {
	switch bits {
	case 8:
		rowBytes := width * spp
		for r := 0; r < rows; r++ {
			row := data[r*rowBytes : (r+1)*rowBytes]
			for i := spp; i < len(row); i++ {
				row[i] += row[i-spp]
			}
		}
	case 16:
		rowVals := width * spp
		for r := 0; r < rows; r++ {
			row := data[r*rowVals*2 : (r+1)*rowVals*2]
			for i := spp; i < rowVals; i++ {
				v := binary.LittleEndian.Uint16(row[i*2:]) + binary.LittleEndian.Uint16(row[(i-spp)*2:])
				binary.LittleEndian.PutUint16(row[i*2:], v)
			}
		}
	}
}
