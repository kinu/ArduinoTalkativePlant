// Reads 16- or 8-bit PCM aiff file and outputs 8-bit PCM data for Arduino.
// Usage:
//  $ go run aiff2arduino.go [-dither] input.aiff > output.h
//
// This generates a header file that looks like:
//   prog_uchar input[] PROGMEM = { 127, 127, ... };
//
// which can be directly included in your Arduino sketch:
//   #include "output.h"
//
// References:
//  http://www-mmsp.ece.mcgill.ca/Documents/AudioFormats/AIFF/Docs/AIFF-1.3.pdf
//  http://www-mmsp.ece.mcgill.ca/Documents/AudioFormats/AIFF/Docs/AIFF-C.9.26.91.pdf

package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path"
	"time"
)

type ID [4]byte

func (id ID) String() string {
	return string(id[:4])
}

type Extended struct {
	Exponent int16
	HiMant uint32
	LoMant uint32
}

func (e Extended) Float64() float64 {
	switch {
	case e.Exponent == 0 && e.HiMant == 0 && e.LoMant == 0:
		return 0.0
	case e.Exponent == 0x7fff:
		return math.MaxFloat64
	default:
		exp := e.Exponent - 16383
		exp -= 31
		f := float64(e.HiMant) * math.Pow(2, float64(exp))
		exp -= 32
		f += float64(e.LoMant) * math.Pow(2, float64(exp))
		return f
	}
}

type ChunkHeader struct {
	Id ID
	Size int32
}

type AIFFChunkHeader struct {
	ChunkHeader
	FormType ID
}

type CommonChunkHeader struct {
	NumChannels int16
	NumSampleFrames uint32
	SampleSize uint16
	SampleRate Extended
}

type CompressionHeader struct {
	CompressionType ID
	CompressionNameSize byte
}

type SoundDataChunkHeader struct {
	Offset uint32
	BlockSize uint32
}

func readInt16(b1, b2 byte) (ret int16, err error) {
	buf := bytes.NewBuffer([]byte{b1, b2})
	err = binary.Read(buf, binary.BigEndian, &ret)
	return
}

func skipBytes(r *bufio.Reader, size int32) {
	for i := int32(0); i < size; i++ {
		r.ReadByte()
	}
}

func scanChunk(r *bufio.Reader, chunkId string) {
	chunk := new(ChunkHeader)
	for {
		if err := binary.Read(r, binary.BigEndian, chunk); err != nil {
			panic(err)
		}
		if chunk.Id.String() == chunkId {
			return
		}
		// Skip padded bytes.
		skipBytes(r, (chunk.Size + 1) &^ 1)
	}
}

func main() {
	var applyDither bool
	flag.BoolVar(&applyDither, "dither", false, "Enable dither for 16bit input")
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		fmt.Printf("Usage: %s [-dither] input.aiff\n", path.Base(os.Args[0]))
		flag.PrintDefaults()
		os.Exit(1)
	}

	f, err := os.Open(args[0])
	if err != nil {
		panic(err)
	}
	defer f.Close()
	r := bufio.NewReader(f)

	arrayName := args[0][:len(args[0]) - len(path.Ext(args[0]))]
	rand.Seed(time.Now().Unix())

	// Parse AIFF container chunk.
	header := new(AIFFChunkHeader)
	if err = binary.Read(r, binary.BigEndian, header); err != nil {
		panic(err)
	}
	if header.Id.String() != "FORM" {
		panic("Invalid file format: Not an AIFF file.")
	}
	if header.FormType.String() != "AIFC" && header.FormType.String() != "AIFF" {
		panic("Invalid file format: Not an AIFF file.")
	}
	hasCompression := header.FormType[3] == 'C'

	// Parse Common chunk.
	scanChunk(r, "COMM")
	common := new(CommonChunkHeader)
	if err = binary.Read(r, binary.BigEndian, common); err != nil {
		panic(err)
	}
	if hasCompression {
		compression := new(CompressionHeader)
		if err = binary.Read(r, binary.BigEndian, compression); err != nil {
			panic(err)
		}
		if compression.CompressionType.String() != "raw " {
			panic("Invalid file format: must be a raw PCM.")
		}
		// The total # of bytes (string length + 1 for size) must be even.
		paddedStringSize := (int32(compression.CompressionNameSize) + 2) &^ 1
		skipBytes(r, paddedStringSize - 1)
	}
	if common.SampleSize != 8 && common.SampleSize != 16 {
		panic("Sample size must be 8-bit or 16-bit.")
	}
	rate := common.SampleRate.Float64()

	// Read out sound data.
	scanChunk(r, "SSND")
	chunk := new(SoundDataChunkHeader)
	if err = binary.Read(r, binary.BigEndian, chunk); err != nil {
		panic(err)
	}
	if chunk.Offset != 0 || chunk.BlockSize != 0 {
		panic("Non-zero offset/blockSize is not supported.")
	}

	fmt.Fprintf(os.Stderr, "Sample rate: %v\n", rate)
	fmt.Fprintf(os.Stderr, "Sample size: %v\n", common.SampleSize)
	fmt.Fprintf(os.Stderr, "Frames: %v\n", common.NumSampleFrames)
	fmt.Fprintf(os.Stderr, "Channels: %v\n", common.NumChannels)

	// For shaped dithering.
	const DITHER_BUF_MASK = 7
	const DITHER_BUF_SIZE = 8
	// Lipshitz's minimally audible FIR
	SHAPED_BS := []float64{ 2.033, -2.165, 1.959, -1.590, 0.6149 }
	buffer := make([]float64, DITHER_BUF_SIZE)
	var idx uint = 0
	noise := func() float64 {
		return float64(rand.Int31()) / float64(math.MaxInt32) - 0.5
	}
	round := func(v float64) float64 {
		return math.Floor(v + 0.5)
	}
	filter := func(v float64) float64 {
		switch {
		case v > 255:
			return 255
		case v < 0:
			return 0
		default:
			return v
		}
	}
	dither := func(v float64, addNoise bool) float64 {
		var xe float64 = v +
			buffer[(idx - 0) & DITHER_BUF_MASK] * SHAPED_BS[0] +
			buffer[(idx - 1) & DITHER_BUF_MASK] * SHAPED_BS[1] +
			buffer[(idx - 2) & DITHER_BUF_MASK] * SHAPED_BS[2] +
			buffer[(idx - 3) & DITHER_BUF_MASK] * SHAPED_BS[3] +
			buffer[(idx - 4) & DITHER_BUF_MASK] * SHAPED_BS[4]
		result := xe
		if addNoise {
			// Triangular noise
			result += noise() + noise()
		}
		result = round(result)
		idx = (idx + 1) & DITHER_BUF_MASK
		buffer[idx] = xe - result
		return result
	}

	fmt.Printf("prog_uchar %s[] PROGMEM = {\n", arrayName)
	for i := uint32(0); i < common.NumSampleFrames; i += 1 {
		b1, err := r.ReadByte()
		if err != nil {
			panic(err)
		}
		switch common.SampleSize {
		case 8:
			fmt.Print(b1)

		case 16:
			b2, err := r.ReadByte()
			if err != nil {
				panic(err)
			}
			v, err := readInt16(b1, b2)
			if err != nil {
				panic(err)
			}

			if applyDither {
				fmt.Print(uint16(filter(dither((float64(v) / 256.0 + 128.0), false))))
			} else {
				fmt.Print(uint16(filter(float64(v) / 256.0 + 128.0)))
			}
		}
		if i < common.NumSampleFrames - 1 {
			fmt.Print(", ")
		}
		for s := int16(0); s < common.NumChannels - 1; s++ {
			// We only use 1 channel (monoral) data per point.
			r.ReadByte()
		}
	}
	fmt.Println("};")
}
