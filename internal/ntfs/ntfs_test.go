package ntfs

import (
	"encoding/binary"
	"testing"
)

// The run-list decoder is the piece most worth pinning: it is compact, it is
// signed, and every one of its offsets is RELATIVE to the run before it. Getting
// any of that wrong still produces plausible-looking cluster numbers, so the
// failure is a read of the wrong part of the volume rather than an error.

func TestParseRunListReadsOneRun(t *testing.T) {
	// 0x21: one length byte, two offset bytes. Length 0x28, offset 0x0334.
	runs, err := parseRunList([]byte{0x21, 0x28, 0x34, 0x03, 0x00})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	if runs[0].clusterCount != 0x28 {
		t.Errorf("count %d, want %d", runs[0].clusterCount, 0x28)
	}
	if runs[0].startCluster != 0x0334 {
		t.Errorf("start %d, want %d", runs[0].startCluster, 0x0334)
	}
}

func TestParseRunListAccumulatesOffsets(t *testing.T) {
	// Two runs: the second's offset is relative to the first's start, so the
	// absolute answer is 0x0334 + 0x0100.
	runs, err := parseRunList([]byte{
		0x21, 0x28, 0x34, 0x03,
		0x21, 0x10, 0x00, 0x01,
		0x00,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	if runs[1].startCluster != 0x0334+0x0100 {
		t.Errorf("second run starts at %d, want %d — offsets accumulate",
			runs[1].startCluster, 0x0334+0x0100)
	}
}

func TestParseRunListHandlesABackwardsJump(t *testing.T) {
	// A later run can sit EARLIER on the disk, which is what the sign is for.
	// 0xFF00 read as two unsigned bytes is +65280; sign-extended it is -256.
	runs, err := parseRunList([]byte{
		0x21, 0x28, 0x00, 0x10, // start at 0x1000
		0x21, 0x10, 0x00, 0xFF, // then -256
		0x00,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	if want := int64(0x1000 - 256); runs[1].startCluster != want {
		t.Errorf("second run starts at %d, want %d — the offset is signed",
			runs[1].startCluster, want)
	}
}

func TestParseRunListRejectsATruncatedList(t *testing.T) {
	// The header promises two offset bytes that are not there.
	if _, err := parseRunList([]byte{0x21, 0x28, 0x34}); err == nil {
		t.Error("expected an error on a truncated run list")
	}
}

func TestParseRunListRejectsAnEmptyList(t *testing.T) {
	if _, err := parseRunList([]byte{0x00}); err == nil {
		t.Error("expected an error when there are no runs at all")
	}
}

// boot builds a 512-byte NTFS boot sector with the fields the reader uses.
func boot(bytesPerSector uint16, sectorsPerCluster uint8, clustersPerRecord int8) []byte {
	raw := make([]byte, 512)
	copy(raw[3:7], "NTFS")
	binary.LittleEndian.PutUint16(raw[11:13], bytesPerSector)
	raw[13] = sectorsPerCluster
	binary.LittleEndian.PutUint64(raw[48:56], 0x0C0000)
	raw[64] = byte(clustersPerRecord)
	return raw
}

func TestParseBootSectorReadsAnOrdinaryVolume(t *testing.T) {
	parsed, err := parseBootSector(boot(512, 8, -10))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.bytesPerSector != 512 || parsed.sectorsPerCluster != 8 {
		t.Errorf("geometry read as %d/%d, want 512/8",
			parsed.bytesPerSector, parsed.sectorsPerCluster)
	}
	if parsed.mftCluster != 0x0C0000 {
		t.Errorf("MFT cluster %#x, want %#x", parsed.mftCluster, 0x0C0000)
	}
	// The field is SIGNED. Read as unsigned, -10 becomes 246 and every offset
	// after it is wrong.
	if parsed.clustersPerRecord != -10 {
		t.Errorf("clustersPerRecord %d, want -10", parsed.clustersPerRecord)
	}
}

func TestParseBootSectorRefusesWhatIsNotNTFS(t *testing.T) {
	raw := boot(512, 8, -10)
	copy(raw[3:7], "FAT3")

	if _, err := parseBootSector(raw); err == nil {
		t.Error("expected an error on a non-NTFS volume")
	}
}

func TestParseBootSectorRefusesImpossibleGeometry(t *testing.T) {
	// A zero would divide by nothing later, and the failure would surface as a
	// nonsensical read offset rather than as a bad boot sector.
	if _, err := parseBootSector(boot(0, 8, -10)); err == nil {
		t.Error("expected an error on a zero-sized sector")
	}
	if _, err := parseBootSector(boot(512, 0, -10)); err == nil {
		t.Error("expected an error on a zero-sized cluster")
	}
}

func TestParseBootSectorRefusesAShortRead(t *testing.T) {
	if _, err := parseBootSector(make([]byte, 100)); err == nil {
		t.Error("expected an error on a short boot sector")
	}
}
