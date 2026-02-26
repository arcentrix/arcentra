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
	"os"
	"path/filepath"
	"sync"
)

const (
	commitMagic   = 0x4F42584F // "OBXO"
	commitVersion = 1
	commitSize    = 4 + 2 + 2 + 8 + 4 // magic + version + reserved + seq + crc32
)

// CommitStore handles atomic read/write of commit.offset.
type CommitStore struct {
	path string
	mu   sync.Mutex
}

// NewCommitStore creates a CommitStore for the given directory.
func NewCommitStore(dir string) *CommitStore {
	return &CommitStore{path: filepath.Join(dir, "commit.offset")}
}

// Read returns the last acked seq. Returns 0 if file does not exist or is invalid.
func (c *CommitStore) Read() (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := os.ReadFile(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if len(data) < commitSize {
		return 0, nil
	}
	magic := binary.BigEndian.Uint32(data[0:4])
	if magic != commitMagic {
		return 0, nil
	}
	version := binary.BigEndian.Uint16(data[4:6])
	if version != commitVersion {
		return 0, nil
	}
	seq := binary.BigEndian.Uint64(data[8:16])
	storedCRC := binary.BigEndian.Uint32(data[16:20])
	computedCRC := crc32.ChecksumIEEE(data[0:16])
	if storedCRC != computedCRC {
		return 0, nil
	}
	return seq, nil
}

// Write atomically writes the last acked seq.
func (c *CommitStore) Write(lastAckedSeq uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	tmpPath := c.path + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	defer func(f *os.File) {
		err = f.Close()
		if err != nil {
			return
		}
	}(f)

	buf := make([]byte, commitSize)
	binary.BigEndian.PutUint32(buf[0:4], commitMagic)
	binary.BigEndian.PutUint16(buf[4:6], commitVersion)
	binary.BigEndian.PutUint16(buf[6:8], 0) // reserved
	binary.BigEndian.PutUint64(buf[8:16], lastAckedSeq)
	crc := crc32.ChecksumIEEE(buf[0:16])
	binary.BigEndian.PutUint32(buf[16:20], crc)

	if _, err := f.Write(buf); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, c.path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	dir := filepath.Dir(c.path)
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	_ = d.Close()
	return err
}
