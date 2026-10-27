package softdist

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// Bound central-directory allocation BEFORE archive/zip builds its member list.
// Pure ZIP/ZIP64 is accepted; split archives and unexplained directory gaps are not.
func inspectBundleDirectory(f *os.File, size int64, maxFiles int) error {
	if size < 22 {
		return fmt.Errorf("invalid ZIP end record")
	}
	n := int64(65535 + 22 + 20)
	if n > size {
		n = size
	}
	tail := make([]byte, int(n))
	if _, err := f.ReadAt(tail, size-n); err != nil {
		return err
	}
	index := -1
	for i := len(tail) - 22; i >= 0; i-- {
		if binary.LittleEndian.Uint32(tail[i:i+4]) == 0x06054b50 && i+22+int(binary.LittleEndian.Uint16(tail[i+20:i+22])) == len(tail) {
			index = i
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("ZIP end record missing")
	}
	end := tail[index:]
	if binary.LittleEndian.Uint16(end[4:6]) != 0 || binary.LittleEndian.Uint16(end[6:8]) != 0 {
		return fmt.Errorf("split ZIP bundle refused")
	}
	count := uint64(binary.LittleEndian.Uint16(end[10:12]))
	directorySize := uint64(binary.LittleEndian.Uint32(end[12:16]))
	offset := uint64(binary.LittleEndian.Uint32(end[16:20]))
	directoryEnd := uint64(size - n + int64(index))
	if binary.LittleEndian.Uint16(end[8:10]) != binary.LittleEndian.Uint16(end[10:12]) {
		return fmt.Errorf("inconsistent ZIP member count")
	}
	if count == 65535 || directorySize == 0xffffffff || offset == 0xffffffff {
		locatorOffset := int64(directoryEnd) - 20
		if locatorOffset < 0 {
			return fmt.Errorf("ZIP64 locator missing")
		}
		locator := make([]byte, 20)
		if _, err := f.ReadAt(locator, locatorOffset); err != nil {
			return err
		}
		if binary.LittleEndian.Uint32(locator[:4]) != 0x07064b50 || binary.LittleEndian.Uint32(locator[4:8]) != 0 || binary.LittleEndian.Uint32(locator[16:20]) != 1 {
			return fmt.Errorf("invalid ZIP64 locator")
		}
		recordOffset := binary.LittleEndian.Uint64(locator[8:16])
		if recordOffset > uint64(locatorOffset) || uint64(locatorOffset)-recordOffset < 56 {
			return fmt.Errorf("invalid ZIP64 end location")
		}
		record := make([]byte, 56)
		if _, err := f.ReadAt(record, int64(recordOffset)); err != nil {
			return err
		}
		if binary.LittleEndian.Uint32(record[:4]) != 0x06064b50 || binary.LittleEndian.Uint32(record[16:20]) != 0 || binary.LittleEndian.Uint32(record[20:24]) != 0 {
			return fmt.Errorf("invalid/split ZIP64 record")
		}
		recordSize := binary.LittleEndian.Uint64(record[4:12])
		if recordSize < 44 || recordSize > 65536 || recordOffset+12+recordSize != uint64(locatorOffset) {
			return fmt.Errorf("invalid ZIP64 record size")
		}
		count = binary.LittleEndian.Uint64(record[32:40])
		if binary.LittleEndian.Uint64(record[24:32]) != count {
			return fmt.Errorf("inconsistent ZIP64 member count")
		}
		directorySize = binary.LittleEndian.Uint64(record[40:48])
		offset = binary.LittleEndian.Uint64(record[48:56])
		directoryEnd = recordOffset
	}
	if count == 0 || count > uint64(maxFiles)+1 || directorySize > 16<<20 || offset > directoryEnd || directorySize != directoryEnd-offset {
		return fmt.Errorf("ZIP directory exceeds member/metadata bounds or has invalid offsets")
	}
	// Count actual headers too: an attacker must not understate the EOCD count
	// and force archive/zip to allocate a much larger member slice.
	reader := bufio.NewReader(io.NewSectionReader(f, int64(offset), int64(directorySize)))
	var used uint64
	var observed uint64
	for used < directorySize {
		var header [46]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			return fmt.Errorf("truncated ZIP directory")
		}
		if binary.LittleEndian.Uint32(header[:4]) != 0x02014b50 {
			return fmt.Errorf("unsupported ZIP directory header")
		}
		variable := uint64(binary.LittleEndian.Uint16(header[28:30])) + uint64(binary.LittleEndian.Uint16(header[30:32])) + uint64(binary.LittleEndian.Uint16(header[32:34]))
		if binary.LittleEndian.Uint16(header[34:36]) != 0 || directorySize-used < 46 || variable > directorySize-used-46 {
			return fmt.Errorf("invalid ZIP directory member")
		}
		observed++
		if observed > uint64(maxFiles)+1 {
			return fmt.Errorf("ZIP actual member count exceeds limit")
		}
		if _, err := io.CopyN(io.Discard, reader, int64(variable)); err != nil {
			return err
		}
		used += 46 + variable
	}
	if observed != count {
		return fmt.Errorf("ZIP declared/actual member count differs")
	}
	return nil
}
