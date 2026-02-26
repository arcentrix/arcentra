// Copyright 2026 Arcentra Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package outbox

import (
	"encoding/binary"
	"hash/crc32"
	"io"
)

const (
	// RecordTypeEvent is the record type for events.
	RecordTypeEvent byte = 0
	// RecordTypeLog is the record type for logs.
	RecordTypeLog byte = 1
	// CodecJSON is the codec flag for JSON payload.
	CodecJSON byte = 0
	// CodecProto is the codec flag for protobuf payload.
	CodecProto byte = 1
)

const (
	recordHeaderSize = 4 + 8 + 1 + 1 + 2 // len + seq + type + flags + reserved
	recordCRCSize    = 4
)

// Record represents a WAL record.
type Record struct {
	Seq     uint64
	Type    byte
	Codec   byte
	Payload []byte
}

// EncodeRecord encodes a record to bytes. Returns the encoded slice.
func EncodeRecord(r *Record) []byte {
	payloadLen := len(r.Payload)
	totalLen := recordHeaderSize + payloadLen + recordCRCSize
	buf := make([]byte, totalLen)
	binary.BigEndian.PutUint32(buf[0:4], uint32(totalLen))
	binary.BigEndian.PutUint64(buf[4:12], r.Seq)
	buf[12] = r.Type
	buf[13] = r.Codec & 0x0F
	buf[14] = 0
	buf[15] = 0
	copy(buf[16:16+payloadLen], r.Payload)
	crc := crc32.ChecksumIEEE(buf[0 : 16+payloadLen])
	binary.BigEndian.PutUint32(buf[16+payloadLen:20+payloadLen], crc)
	return buf
}

// DecodeRecord decodes a record from bytes. Returns nil if invalid.
func DecodeRecord(data []byte) *Record {
	if len(data) < recordHeaderSize+recordCRCSize {
		return nil
	}
	totalLen := binary.BigEndian.Uint32(data[0:4])
	if uint32(len(data)) < totalLen {
		return nil
	}
	payloadLen := totalLen - recordHeaderSize - recordCRCSize
	storedCRC := binary.BigEndian.Uint32(data[totalLen-recordCRCSize : totalLen])
	computedCRC := crc32.ChecksumIEEE(data[0 : totalLen-recordCRCSize])
	if storedCRC != computedCRC {
		return nil
	}
	return &Record{
		Seq:     binary.BigEndian.Uint64(data[4:12]),
		Type:    data[12],
		Codec:   data[13] & 0x0F,
		Payload: append([]byte(nil), data[16:16+payloadLen]...),
	}
}

// ReadNextRecord reads the next record from r. Returns nil when no more or error.
func ReadNextRecord(r io.Reader) (*Record, error) {
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(r, lenBuf); err != nil {
		return nil, err
	}
	totalLen := binary.BigEndian.Uint32(lenBuf)
	if totalLen < recordHeaderSize+recordCRCSize || totalLen > 64*1024*1024 {
		return nil, nil
	}
	buf := make([]byte, totalLen)
	buf[0] = lenBuf[0]
	buf[1] = lenBuf[1]
	buf[2] = lenBuf[2]
	buf[3] = lenBuf[3]
	if _, err := io.ReadFull(r, buf[4:]); err != nil {
		return nil, err
	}
	return DecodeRecord(buf), nil
}
