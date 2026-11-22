package art

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dhowden/tag"
)

// Info is the technical side of a track, for the fullscreen player. Zero
// fields mean "unknown" and are left out of the JSON.
type Info struct {
	Format      string `json:"format,omitempty"`
	BitrateKbps int    `json:"bitrate_kbps,omitempty"`
	BitDepth    int    `json:"bit_depth,omitempty"`
	SampleRate  int    `json:"sample_rate,omitempty"`
	Channels    int    `json:"channels,omitempty"`
}

// ErrNoInfo means nothing is known (or the path is refused).
var ErrNoInfo = errors.New("art: no info")

// Info reads what the file itself says: the FLAC header in full, otherwise the
// tag type plus an average bitrate when the duration is known. The same
// refusals as art apply: only tracks the bridge was told about.
func (l *Local) Info(trackPath string, durationSecs float64) (Info, error) {
	if trackPath == "" || !filepath.IsAbs(trackPath) || filepath.Clean(trackPath) != trackPath ||
		l.Known == nil || !l.Known(trackPath) {
		return Info{}, ErrNoInfo
	}
	f, err := os.Open(trackPath)
	if err != nil {
		return Info{}, ErrNoInfo
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return Info{}, ErrNoInfo
	}
	if info, ok := flacInfo(f); ok {
		return info, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Info{}, ErrNoInfo
	}
	m, err := tag.ReadFrom(f)
	if err != nil {
		return Info{}, ErrNoInfo
	}
	info := Info{Format: strings.ToUpper(string(m.FileType()))}
	if durationSecs > 0 {
		info.BitrateKbps = int(float64(fi.Size()) * 8 / durationSecs / 1000)
	}
	return info, nil
}

// flacInfo parses the STREAMINFO block that must follow the "fLaC" marker.
func flacInfo(r io.Reader) (Info, bool) {
	var b [4 + 4 + 34]byte
	if _, err := io.ReadFull(r, b[:]); err != nil || string(b[:4]) != "fLaC" || b[4]&0x7f != 0 {
		return Info{}, false
	}
	v := binary.BigEndian.Uint64(b[8+10:]) // after the 4+4 header and 10 bytes of block/frame sizes
	return Info{
		Format:     "FLAC",
		SampleRate: int(v >> 44),
		Channels:   int(v>>41&0x7) + 1,
		BitDepth:   int(v>>36&0x1f) + 1,
	}, true
}
