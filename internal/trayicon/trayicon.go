// Package trayicon generates SmartEfficiency's tray icon at build time from
// plain Go drawing code rather than shipping a binary asset file - a simple
// green ring/leaf motif (efficiency/battery association). PNG is used
// directly on Linux/macOS; Windows needs an .ico container, which is built
// by wrapping the same PNG bytes in a minimal single-image ICO header (a
// well-documented shortcut Windows Shell fully supports - no need to hand-
// encode a raw BMP/DIB).
package trayicon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

const size = 32

// PNG renders the icon as PNG bytes - the format systray expects on
// Linux/macOS, and what Windows' ICO wrapper below embeds.
func PNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	center := float64(size) / 2
	outerR := float64(size)/2 - 1
	innerR := outerR * 0.55

	ringColor := color.RGBA{0x22, 0xc5, 0x5e, 0xff} // green
	dotColor := color.RGBA{0x16, 0x8a, 0x40, 0xff}   // darker green

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx := float64(x) + 0.5 - center
			dy := float64(y) + 0.5 - center
			dist := math.Sqrt(dx*dx + dy*dy)
			switch {
			case dist <= innerR:
				img.Set(x, y, dotColor)
			case dist <= outerR:
				img.Set(x, y, ringColor)
			default:
				img.Set(x, y, color.RGBA{0, 0, 0, 0}) // transparent
			}
		}
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// ICO wraps PNG() in a minimal single-image ICO container. Modern Windows
// Shell (Vista+) accepts a PNG payload directly inside an ICO's image data,
// so this is just the 6-byte ICONDIR + 16-byte ICONDIRENTRY header glued in
// front of the PNG bytes - no BMP/DIB encoding needed.
func ICO() []byte {
	png := PNG()

	var buf bytes.Buffer
	// ICONDIR: reserved(2)=0, type(2)=1 (icon), count(2)=1
	binary.Write(&buf, binary.LittleEndian, uint16(0))
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	binary.Write(&buf, binary.LittleEndian, uint16(1))

	// ICONDIRENTRY
	buf.WriteByte(size) // width (32 fits in one byte; 256 would need 0)
	buf.WriteByte(size) // height
	buf.WriteByte(0)    // color count (0 = no palette)
	buf.WriteByte(0)    // reserved
	binary.Write(&buf, binary.LittleEndian, uint16(1))  // color planes
	binary.Write(&buf, binary.LittleEndian, uint16(32)) // bits per pixel
	binary.Write(&buf, binary.LittleEndian, uint32(len(png)))
	binary.Write(&buf, binary.LittleEndian, uint32(22)) // offset: 6 + 16 = 22

	buf.Write(png)
	return buf.Bytes()
}
