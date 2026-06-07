package tsdb

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Block file format (immutable, written once at window close):
//
//	[8]  magic "NPBLOCK1"
//	[8]  window start (unix ms, BE)
//	[8]  window end
//	[4]  series count N
//	N ×  [8 seriesID][4 offset][4 length][4 sampleCount]
//	data section (concatenated Gorilla payloads)
//
// Aggregate file format (one day window):
//
//	[8]  magic "NPAGGR1\0"
//	[8]  day start (unix ms)
//	[4]  bucket ms
//	[4]  series count N
//	N ×  [8 seriesID][4 offset][4 recordCount]
//	data: records of [4 bucketIdx][4 count][8 sumBits][8 minBits][8 maxBits]

var (
	blockMagic = [8]byte{'N', 'P', 'B', 'L', 'O', 'C', 'K', '1'}
	aggMagic   = [8]byte{'N', 'P', 'A', 'G', 'G', 'R', '1', 0}
)

// syncDir fsyncs the directory holding path so a rename into it is
// durable across a crash (the file contents are already fsync'd, but the
// new directory entry is not until the parent dir is synced).
func syncDir(path string) error {
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func writeBlockFile(path string, ws, we int64, entries []blockEntry) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	w.Write(blockMagic[:])
	binary.Write(w, binary.BigEndian, ws)
	binary.Write(w, binary.BigEndian, we)
	binary.Write(w, binary.BigEndian, uint32(len(entries)))
	offset := uint32(0)
	for _, e := range entries {
		binary.Write(w, binary.BigEndian, e.seriesID)
		binary.Write(w, binary.BigEndian, offset)
		binary.Write(w, binary.BigEndian, uint32(len(e.payload)))
		binary.Write(w, binary.BigEndian, e.count)
		offset += uint32(len(e.payload))
	}
	for _, e := range entries {
		w.Write(e.payload)
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	f.Close()
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(path)
}

// blockIndexEntry locates a series inside a block.
type blockIndexEntry struct {
	offset uint32
	length uint32
	count  uint32
}

// readBlockIndex loads header + index of a block file.
func readBlockIndex(path string) (map[uint64]blockIndexEntry, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	var head [28]byte
	if _, err := io.ReadFull(f, head[:]); err != nil {
		return nil, 0, err
	}
	if [8]byte(head[:8]) != blockMagic {
		return nil, 0, fmt.Errorf("tsdb: %s: bad magic", path)
	}
	ws := int64(binary.BigEndian.Uint64(head[8:16]))
	n := binary.BigEndian.Uint32(head[24:28])
	// Reject a corrupt/forged count before allocating: 20 bytes per index
	// entry must fit in what remains after the 28-byte header. int64 math
	// avoids uint32 overflow.
	if 28+int64(n)*20 > fi.Size() {
		return nil, 0, fmt.Errorf("tsdb: %s: index count %d exceeds file size", path, n)
	}
	idx := make(map[uint64]blockIndexEntry, n)
	buf := make([]byte, 20*int64(n))
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, 0, err
	}
	for i := uint32(0); i < n; i++ {
		o := i * 20
		idx[binary.BigEndian.Uint64(buf[o:o+8])] = blockIndexEntry{
			offset: binary.BigEndian.Uint32(buf[o+8 : o+12]),
			length: binary.BigEndian.Uint32(buf[o+12 : o+16]),
			count:  binary.BigEndian.Uint32(buf[o+16 : o+20]),
		}
	}
	return idx, ws, nil
}

// readBlockSeries decodes one series' samples from a block file.
func readBlockSeries(path string, idx map[uint64]blockIndexEntry, headerSize int64, seriesID uint64) ([]Sample, error) {
	e, ok := idx[seriesID]
	if !ok {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if headerSize+int64(e.offset)+int64(e.length) > fi.Size() {
		return nil, fmt.Errorf("tsdb: %s: series extent out of bounds", path)
	}
	payload := make([]byte, e.length)
	if _, err := f.ReadAt(payload, headerSize+int64(e.offset)); err != nil {
		return nil, err
	}
	return DecodeChunk(payload, e.count)
}

func blockHeaderSize(seriesCount int) int64 { return 28 + int64(seriesCount)*20 }

// AggRecord is one downsampled bucket.
type AggRecord struct {
	BucketIdx uint32
	Count     uint32
	Sum       float64
	Min       float64
	Max       float64
}

type aggEntry struct {
	seriesID uint64
	records  []AggRecord
}

func writeAggFile(path string, dayStart int64, bucketMS uint32, entries []aggEntry) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	w.Write(aggMagic[:])
	binary.Write(w, binary.BigEndian, dayStart)
	binary.Write(w, binary.BigEndian, bucketMS)
	binary.Write(w, binary.BigEndian, uint32(len(entries)))
	offset := uint32(0)
	for _, e := range entries {
		binary.Write(w, binary.BigEndian, e.seriesID)
		binary.Write(w, binary.BigEndian, offset)
		binary.Write(w, binary.BigEndian, uint32(len(e.records)))
		offset += uint32(len(e.records)) * 32
	}
	for _, e := range entries {
		for _, rec := range e.records {
			binary.Write(w, binary.BigEndian, rec.BucketIdx)
			binary.Write(w, binary.BigEndian, rec.Count)
			binary.Write(w, binary.BigEndian, mathFloat64bits(rec.Sum))
			binary.Write(w, binary.BigEndian, mathFloat64bits(rec.Min))
			binary.Write(w, binary.BigEndian, mathFloat64bits(rec.Max))
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	f.Close()
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(path)
}

type aggIndexEntry struct {
	offset uint32
	count  uint32
}

func readAggIndex(path string) (map[uint64]aggIndexEntry, int64, uint32, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, 0, 0, err
	}
	var head [24]byte
	if _, err := io.ReadFull(f, head[:]); err != nil {
		return nil, 0, 0, err
	}
	if [8]byte(head[:8]) != aggMagic {
		return nil, 0, 0, fmt.Errorf("tsdb: %s: bad agg magic", path)
	}
	dayStart := int64(binary.BigEndian.Uint64(head[8:16]))
	bucketMS := binary.BigEndian.Uint32(head[16:20])
	n := binary.BigEndian.Uint32(head[20:24])
	if 24+int64(n)*16 > fi.Size() {
		return nil, 0, 0, fmt.Errorf("tsdb: %s: agg index count %d exceeds file size", path, n)
	}
	idx := make(map[uint64]aggIndexEntry, n)
	buf := make([]byte, 16*int64(n))
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, 0, 0, err
	}
	for i := uint32(0); i < n; i++ {
		o := i * 16
		idx[binary.BigEndian.Uint64(buf[o:o+8])] = aggIndexEntry{
			offset: binary.BigEndian.Uint32(buf[o+8 : o+12]),
			count:  binary.BigEndian.Uint32(buf[o+12 : o+16]),
		}
	}
	_ = dayStart
	return idx, dayStart, bucketMS, nil
}

func aggHeaderSize(seriesCount int) int64 { return 24 + int64(seriesCount)*16 }

func readAggSeries(path string, idx map[uint64]aggIndexEntry, headerSize int64, seriesID uint64) ([]AggRecord, error) {
	e, ok := idx[seriesID]
	if !ok {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if headerSize+int64(e.offset)+int64(e.count)*32 > fi.Size() {
		return nil, fmt.Errorf("tsdb: %s: agg series extent out of bounds", path)
	}
	buf := make([]byte, int64(e.count)*32)
	if _, err := f.ReadAt(buf, headerSize+int64(e.offset)); err != nil {
		return nil, err
	}
	out := make([]AggRecord, e.count)
	for i := range out {
		o := i * 32
		out[i] = AggRecord{
			BucketIdx: binary.BigEndian.Uint32(buf[o : o+4]),
			Count:     binary.BigEndian.Uint32(buf[o+4 : o+8]),
			Sum:       mathFloat64frombits(binary.BigEndian.Uint64(buf[o+8 : o+16])),
			Min:       mathFloat64frombits(binary.BigEndian.Uint64(buf[o+16 : o+24])),
			Max:       mathFloat64frombits(binary.BigEndian.Uint64(buf[o+24 : o+32])),
		}
	}
	return out, nil
}
