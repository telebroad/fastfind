package ntfs

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// Reading the Master File Table directly, which is the whole trick.
//
// A directory walk asks the filesystem a question per directory and gets a
// handful of names back each time; on a drive with two million files that is
// millions of round trips, each one seeking somewhere else on disk. NTFS however
// already keeps a single flat table describing every file on the volume — the
// MFT — with one fixed-size record per file. Reading it is one long sequential
// scan of a few hundred megabytes.
//
// That is the entire difference between this and `find`. Not a cleverer search:
// a different source. WizFile and Everything both do the same thing.
//
// The cost is that the table is only reachable through the raw volume, which
// Windows only opens for an administrator. That is why those tools ask to
// elevate, and why this one does too.

const (
	// One MFT record begins with this, and a record that does not is either
	// unused or something has gone wrong with the run list.
	recordMagic = "FILE"

	attrStandardInfo = 0x10
	attrFileName     = 0x30
	attrData         = 0x80
	attrEnd          = 0xFFFFFFFF

	// Record header flags.
	flagInUse     = 0x0001
	flagDirectory = 0x0002

	// $FILE_NAME namespaces. A file can carry several names — a long one and
	// the old 8.3 short one — and listing both would double every result.
	nameSpacePosix = 0
	nameSpaceWin32 = 1
	nameSpaceDOS   = 2
	nameSpaceBoth  = 3

	// The record number the root directory always has.
	rootRecord = 5
)

// Volume is an open raw NTFS volume, positioned to read its MFT.
type Volume struct {
	handle       *os.File
	bytesPerSect uint32
	sectsPerClus uint32
	bytesPerClus uint32
	recordSize   uint32
	mftStart     int64 // byte offset of the MFT's first cluster
}

// bootSector is the part of the NTFS BPB this needs.
type bootSector struct {
	bytesPerSector    uint16
	sectorsPerCluster uint8
	mftCluster        uint64
	clustersPerRecord int8
}

// Open takes a drive letter and returns the volume ready to read.
//
// The path form is the raw device one — `\\.\C:` — because a normal open of
// `C:\` gives the filesystem, and the MFT is underneath it. Note there is no
// trailing backslash: with one, Windows opens the root directory instead and the
// reads fail in a way that looks like a corrupt volume rather than a wrong path.
func Open(drive byte) (*Volume, error) {
	path := fmt.Sprintf(`\\.\%c:`, drive)

	handle, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w (raw volume access needs an elevated prompt)", path, err)
	}

	raw := make([]byte, 512)
	if _, err := io.ReadFull(handle, raw); err != nil {
		handle.Close()
		return nil, fmt.Errorf("reading the boot sector of %s: %w", path, err)
	}

	boot, err := parseBootSector(raw)
	if err != nil {
		handle.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	bytesPerClus := uint32(boot.bytesPerSector) * uint32(boot.sectorsPerCluster)

	// The field is signed on purpose. A positive value counts clusters per
	// record; a negative one is a shift, so -10 means 2^10 = 1024 bytes, which
	// is what every ordinary volume uses. Reading it as unsigned gives a record
	// size of 246 and every subsequent offset is wrong.
	var recordSize uint32
	if boot.clustersPerRecord >= 0 {
		recordSize = uint32(boot.clustersPerRecord) * bytesPerClus
	} else {
		recordSize = 1 << uint(-boot.clustersPerRecord)
	}

	return &Volume{
		handle:       handle,
		bytesPerSect: uint32(boot.bytesPerSector),
		sectsPerClus: uint32(boot.sectorsPerCluster),
		bytesPerClus: bytesPerClus,
		recordSize:   recordSize,
		mftStart:     int64(boot.mftCluster) * int64(bytesPerClus),
	}, nil
}

func (v *Volume) Close() error { return v.handle.Close() }

func parseBootSector(raw []byte) (bootSector, error) {
	if len(raw) < 512 {
		return bootSector{}, fmt.Errorf("short boot sector")
	}
	if string(raw[3:7]) != "NTFS" {
		return bootSector{}, fmt.Errorf("not an NTFS volume")
	}

	boot := bootSector{
		bytesPerSector:    binary.LittleEndian.Uint16(raw[11:13]),
		sectorsPerCluster: raw[13],
		mftCluster:        binary.LittleEndian.Uint64(raw[48:56]),
		clustersPerRecord: int8(raw[64]),
	}

	if boot.bytesPerSector == 0 || boot.sectorsPerCluster == 0 {
		return bootSector{}, fmt.Errorf("boot sector reports a zero-sized sector or cluster")
	}
	return boot, nil
}

// readAt reads exactly len(into) bytes from a byte offset on the volume.
//
// Raw volume reads must be sector-aligned in both offset and length, so this
// widens the request to sector boundaries and returns the slice that was
// actually asked for. Without this every unaligned read fails with a bare
// "Incorrect function", which is a remarkably unhelpful way to be told about
// alignment.
func (v *Volume) readAt(into []byte, offset int64) error {
	sector := int64(v.bytesPerSect)
	start := offset - offset%sector
	end := offset + int64(len(into))
	if rem := end % sector; rem != 0 {
		end += sector - rem
	}

	buffer := make([]byte, end-start)
	if _, err := v.handle.ReadAt(buffer, start); err != nil {
		return err
	}

	copy(into, buffer[offset-start:])
	return nil
}

// applyFixups repairs a record read straight off the disk.
//
// NTFS overwrites the last two bytes of every sector in a record with a counter
// and keeps the real bytes in an array at the top of the record. It is a torn-
// write detector: if the counters do not all match, the record was caught
// mid-write. The originals have to be put back before anything in the record can
// be trusted — skip this and attribute lengths read as sequence numbers and the
// parse walks off into nonsense.
func (v *Volume) applyFixups(record []byte) error {
	if len(record) < 8 {
		return fmt.Errorf("record too short for a fixup header")
	}

	offset := int(binary.LittleEndian.Uint16(record[4:6]))
	count := int(binary.LittleEndian.Uint16(record[6:8]))
	if count == 0 || offset+count*2 > len(record) {
		return fmt.Errorf("fixup array out of range")
	}

	expected := record[offset : offset+2]
	sector := int(v.bytesPerSect)

	// Entry 0 is the counter itself; the rest are the saved bytes, one per
	// sector.
	for i := 1; i < count; i++ {
		tail := i*sector - 2
		if tail+2 > len(record) {
			return fmt.Errorf("fixup points past the end of the record")
		}
		if record[tail] != expected[0] || record[tail+1] != expected[1] {
			return fmt.Errorf("fixup mismatch in sector %d: the record was torn mid-write", i)
		}
		saved := record[offset+i*2 : offset+i*2+2]
		record[tail], record[tail+1] = saved[0], saved[1]
	}
	return nil
}

// dataRun is one contiguous stretch of the MFT on disk.
type dataRun struct {
	startCluster int64
	clusterCount int64
}

// readMFTRuns finds where the MFT actually lives.
//
// The MFT is itself a file, described by record 0 of the MFT — which is why this
// has to read that one record the hard way first, from the offset the boot
// sector gave, and then follow its $DATA run list to reach the rest. The table
// is rarely one contiguous stretch on a drive that has been in use.
func (v *Volume) readMFTRuns() ([]dataRun, error) {
	record := make([]byte, v.recordSize)
	if err := v.readAt(record, v.mftStart); err != nil {
		return nil, fmt.Errorf("reading MFT record 0: %w", err)
	}
	if string(record[0:4]) != recordMagic {
		return nil, fmt.Errorf("MFT record 0 is not a file record — wrong offset or not NTFS")
	}
	if err := v.applyFixups(record); err != nil {
		return nil, fmt.Errorf("MFT record 0: %w", err)
	}

	attrOffset := int(binary.LittleEndian.Uint16(record[20:22]))
	for attrOffset+8 <= len(record) {
		attrType := binary.LittleEndian.Uint32(record[attrOffset : attrOffset+4])
		if attrType == attrEnd {
			break
		}
		attrLen := int(binary.LittleEndian.Uint32(record[attrOffset+4 : attrOffset+8]))
		if attrLen <= 0 || attrOffset+attrLen > len(record) {
			break
		}

		if attrType == attrData {
			// The MFT's own data is always non-resident: it is far too big to
			// sit inside a 1KB record.
			if record[attrOffset+8] == 0 {
				return nil, fmt.Errorf("the MFT's $DATA is resident, which cannot be right")
			}
			runOffset := int(binary.LittleEndian.Uint16(record[attrOffset+32 : attrOffset+34]))
			return parseRunList(record[attrOffset+runOffset : attrOffset+attrLen])
		}
		attrOffset += attrLen
	}
	return nil, fmt.Errorf("no $DATA attribute on MFT record 0")
}

// parseRunList decodes NTFS's compact description of where a file's bytes are.
//
// Each run starts with one byte packing two nibble-sized lengths: how many bytes
// hold the run's length, and how many hold its offset. The offset is SIGNED and
// RELATIVE to the previous run's start — a later run can sit earlier on the disk
// — so it is sign-extended and accumulated. Treating it as absolute reads a
// plausible-looking but wrong part of the volume.
func parseRunList(raw []byte) ([]dataRun, error) {
	var runs []dataRun
	var previous int64
	at := 0

	for at < len(raw) && raw[at] != 0 {
		header := raw[at]
		lengthBytes := int(header & 0x0F)
		offsetBytes := int(header >> 4)
		at++

		if lengthBytes == 0 || at+lengthBytes+offsetBytes > len(raw) {
			return nil, fmt.Errorf("truncated run list")
		}

		count := int64(0)
		for i := lengthBytes - 1; i >= 0; i-- {
			count = count<<8 | int64(raw[at+i])
		}
		at += lengthBytes

		// A zero offset length is a sparse run: it occupies logical space but no
		// disk. The MFT should not have one, but skipping rather than trusting
		// it keeps a damaged volume from producing garbage paths.
		if offsetBytes == 0 {
			at += offsetBytes
			continue
		}

		offset := int64(0)
		for i := offsetBytes - 1; i >= 0; i-- {
			offset = offset<<8 | int64(raw[at+i])
		}
		// Sign-extend from however many bytes it actually used.
		if shift := uint(64 - offsetBytes*8); offset&(1<<(uint(offsetBytes*8)-1)) != 0 {
			offset = offset << shift >> shift
		}
		at += offsetBytes

		previous += offset
		runs = append(runs, dataRun{startCluster: previous, clusterCount: count})
	}

	if len(runs) == 0 {
		return nil, fmt.Errorf("empty run list")
	}
	return runs, nil
}
