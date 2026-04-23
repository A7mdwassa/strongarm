//grdp/core/io.go
package core

import (
	"encoding/binary"
	"io"
)

type ReadBytesComplete func(result []byte, err error)

func StartReadBytes(len int, r io.Reader, cb ReadBytesComplete) {
	const maxGRDPSegment = 65535 // TCP MSS limit; also covers UDP MTU for safety

	// Bounds checking to prevent panic on malformed/corrupted length values.
	// GRDP runs over UDP/TPKT but uses a conservative bound matching the underlying transport's limits.
	if len <= 0 || len > maxGRDPSegment {
		go func() {
			cb(nil, io.ErrUnexpectedEOF)
		}()
		return
	}
	
	b := make([]byte, len)
	go func() {
		_, err := io.ReadFull(r, b)
		//glog.Debug("StartReadBytes Get", n, "Bytes:", hex.EncodeToString(b))
		cb(b, err)
	}()
}

func ReadBytes(len int, r io.Reader) ([]byte, error) {
	// Bounds checking
	if len <= 0 || len > 65535 {
		return nil, io.ErrUnexpectedEOF
	}
	
	b := make([]byte, len)
	length, err := io.ReadFull(r, b)
	return b[:length], err
}

func ReadByte(r io.Reader) (byte, error) {
	b, err := ReadBytes(1, r)
	return b[0], err
}

func ReadUInt8(r io.Reader) (uint8, error) {
	b, err := ReadBytes(1, r)
	return uint8(b[0]), err
}

func ReadUint16LE(r io.Reader) (uint16, error) {
	b := make([]byte, 2)
	n, err := io.ReadFull(r, b)
	if n < 2 || err != nil {
		return binary.LittleEndian.Uint16(b), err
	}
	return binary.LittleEndian.Uint16(b), nil
}

func ReadUint16BE(r io.Reader) (uint16, error) {
	b := make([]byte, 2)
	n, err := io.ReadFull(r, b)
	if n < 2 || err != nil {
		return binary.BigEndian.Uint16(b), err
	}
	return binary.BigEndian.Uint16(b), nil
}

func ReadUInt32LE(r io.Reader) (uint32, error) {
	b := make([]byte, 4)
	n, err := io.ReadFull(r, b)
	if n < 4 || err != nil {
		return binary.LittleEndian.Uint32(b), err
	}
	return binary.LittleEndian.Uint32(b), nil
}

func ReadUInt32BE(r io.Reader) (uint32, error) {
	b := make([]byte, 4)
	n, err := io.ReadFull(r, b)
	if n < 4 || err != nil {
		return binary.BigEndian.Uint32(b), err
	}
	return binary.BigEndian.Uint32(b), nil
}

// writeBuf1 is a reused buffer for single-byte writes to reduce heap allocations.
var writeBuf1 [1]byte

func WriteByte(data byte, w io.Writer) (int, error) {
	writeBuf1[0] = data
	return w.Write(writeBuf1[:])
}

func WriteBytes(data []byte, w io.Writer) (int, error) {
	return w.Write(data)
}

// writeBuf2 is a reused buffer for 2-byte writes.
var writeBuf2 [2]byte

func WriteUInt8(data uint8, w io.Writer) (int, error) {
	writeBuf2[0] = byte(data)
	return w.Write(writeBuf2[:])
}

func WriteUInt16BE(data uint16, w io.Writer) (int, error) {
	b := binary.BigEndian.AppendUint16(nil, data)
	return w.Write(b)
}

func WriteUInt16LE(data uint16, w io.Writer) (int, error) {
	b := binary.LittleEndian.AppendUint16(nil, data)
	return w.Write(b)
}

// writeBuf4 is a reused buffer for 4-byte writes.
var writeBuf4 [4]byte

func WriteUInt32LE(data uint32, w io.Writer) (int, error) {
	b := binary.LittleEndian.AppendUint32(nil, data)
	return w.Write(b)
}

func WriteUInt32BE(data uint32, w io.Writer) (int, error) {
	b := binary.BigEndian.AppendUint32(nil, data)
	return w.Write(b)
}

func PutUint16BE(data uint16) (uint8, uint8) {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, data)
	return uint8(b[0]), uint8(b[1])
}

func Uint16BE(d0, d1 uint8) uint16 {
	b := make([]byte, 2)
	b[0] = d0
	b[1] = d1

	return binary.BigEndian.Uint16(b)
}

func RGB565ToRGB(data uint16) (r, g, b uint8) {
	r = uint8(uint32(data&0xF800)>>11) << 3
	g = uint8(uint32(data&0x07E0)>>5) << 2
	b = uint8(uint32(data&0x001F)) << 3

	return
}
