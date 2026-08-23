package tts

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"time"
)

// Audio is decoded speech: signed 16-bit mono PCM at Rate Hz. Every
// engine output is brought into this shape first (WAV, MP3, raw PCM,
// G.711 are all decoded), so post-processing (resampling, loudness,
// silence padding, pre-roll tones, concatenation of multi-language
// segments) and the output encoders work on exactly one representation.
type Audio struct {
	Rate int
	PCM  []int16
}

// Duration of the clip.
func (a *Audio) Duration() time.Duration {
	if a == nil || a.Rate <= 0 {
		return 0
	}
	return time.Duration(float64(len(a.PCM)) / float64(a.Rate) * float64(time.Second))
}

// Clone returns a deep copy.
func (a *Audio) Clone() *Audio {
	return &Audio{Rate: a.Rate, PCM: append([]int16(nil), a.PCM...)}
}

// Silence returns a silent clip.
func Silence(rate int, d time.Duration) *Audio {
	n := int(float64(rate) * d.Seconds())
	if n < 0 {
		n = 0
	}
	return &Audio{Rate: rate, PCM: make([]int16, n)}
}

// Tone synthesises a sine tone with a short fade in/out (no clicks).
// amp is the linear amplitude 0..1.
func Tone(rate int, freq float64, d time.Duration, amp float64) *Audio {
	n := int(float64(rate) * d.Seconds())
	out := make([]int16, n)
	fade := rate / 100 // 10 ms
	if fade*2 > n {
		fade = n / 2
	}
	for i := range out {
		v := math.Sin(2*math.Pi*freq*float64(i)/float64(rate)) * amp
		switch {
		case i < fade:
			v *= float64(i) / float64(fade)
		case i >= n-fade:
			v *= float64(n-i) / float64(fade)
		}
		out[i] = int16(v * 32767)
	}
	return &Audio{Rate: rate, PCM: out}
}

// Preroll names the built-in attention signals that can precede an
// announcement so the called person immediately recognises an alarm
// call: "chime" (two-tone ding-dong), "alert" (three short rising
// beeps), "gong" (one long low tone). "" or "none" = no pre-roll.
func Preroll(name string, rate int) *Audio {
	switch name {
	case "chime":
		return Concat(
			Tone(rate, 880, 250*time.Millisecond, 0.6),
			Silence(rate, 60*time.Millisecond),
			Tone(rate, 660, 350*time.Millisecond, 0.6),
		)
	case "alert":
		return Concat(
			Tone(rate, 660, 140*time.Millisecond, 0.6), Silence(rate, 70*time.Millisecond),
			Tone(rate, 880, 140*time.Millisecond, 0.6), Silence(rate, 70*time.Millisecond),
			Tone(rate, 1100, 220*time.Millisecond, 0.6),
		)
	case "gong":
		return Tone(rate, 440, 700*time.Millisecond, 0.55)
	}
	return nil
}

// PrerollNames lists the selectable pre-roll signals.
var PrerollNames = []string{"none", "chime", "alert", "gong"}

// Concat joins clips; parts at a different rate are resampled to the
// first part's rate. nil parts are skipped.
func Concat(parts ...*Audio) *Audio {
	var out *Audio
	for _, p := range parts {
		if p == nil || len(p.PCM) == 0 {
			continue
		}
		if out == nil {
			out = &Audio{Rate: p.Rate, PCM: append([]int16(nil), p.PCM...)}
			continue
		}
		if p.Rate != out.Rate {
			p = p.Resample(out.Rate)
		}
		out.PCM = append(out.PCM, p.PCM...)
	}
	if out == nil {
		return &Audio{Rate: 8000}
	}
	return out
}

// Resample converts to another sample rate with a windowed-sinc
// low-pass interpolator (Hann window). When downsampling the cutoff is
// placed below the new Nyquist frequency so telephony 8 kHz output does
// not alias the 24 kHz engine material into audible garbage.
func (a *Audio) Resample(rate int) *Audio {
	if a.Rate == rate || rate <= 0 || a.Rate <= 0 || len(a.PCM) == 0 {
		return &Audio{Rate: rate, PCM: append([]int16(nil), a.PCM...)}
	}
	ratio := float64(a.Rate) / float64(rate) // input samples per output sample
	fc := 0.5                                // cutoff as fraction of input rate (Nyquist)
	if ratio > 1 {
		fc = 0.5 / ratio
	}
	fc *= 0.92 // guard band below Nyquist
	lobes := 10.0
	half := int(math.Ceil(lobes * math.Max(ratio, 1)))
	n := int(math.Floor(float64(len(a.PCM)) / ratio))
	out := make([]int16, n)
	in := a.PCM
	for i := range out {
		center := float64(i) * ratio
		c := int(center)
		var sum, wsum float64
		for k := c - half; k <= c+half+1; k++ {
			if k < 0 || k >= len(in) {
				continue
			}
			x := float64(k) - center
			var h float64
			if x == 0 {
				h = 2 * fc
			} else {
				h = math.Sin(2*math.Pi*fc*x) / (math.Pi * x)
			}
			w := 0.5 + 0.5*math.Cos(math.Pi*x/float64(half+1))
			if w < 0 {
				w = 0
			}
			hw := h * w
			sum += float64(in[k]) * hw
			wsum += hw
		}
		if wsum != 0 {
			sum /= wsum
		}
		out[i] = clip16(sum)
	}
	return &Audio{Rate: rate, PCM: out}
}

func clip16(v float64) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(math.Round(v))
}

// Gain applies a linear gain in dB (clipped).
func (a *Audio) Gain(db float64) {
	if db == 0 {
		return
	}
	g := math.Pow(10, db/20)
	for i, s := range a.PCM {
		a.PCM[i] = clip16(float64(s) * g)
	}
}

// Peak returns the absolute peak sample (0..32768).
func (a *Audio) Peak() int {
	peak := 0
	for _, s := range a.PCM {
		v := int(s)
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	return peak
}

// NormalizePeak scales the clip so its peak sits at targetDBFS (e.g.
// -3 dBFS): cloud engines deliver anything between -1 and -20 dBFS and a
// quiet announcement on a phone line is unintelligible.
func (a *Audio) NormalizePeak(targetDBFS float64) {
	peak := a.Peak()
	if peak == 0 {
		return
	}
	target := math.Pow(10, targetDBFS/20) * 32767
	a.Gain(20 * math.Log10(target/float64(peak)))
}

// TrimSilence strips leading/trailing samples below thresholdDBFS
// (e.g. -45), keeping a small margin so the first phoneme is not cut.
func (a *Audio) TrimSilence(thresholdDBFS float64) {
	if len(a.PCM) == 0 {
		return
	}
	thr := int16(math.Pow(10, thresholdDBFS/20) * 32767)
	start, end := 0, len(a.PCM)
	for start < end && abs16(a.PCM[start]) < thr {
		start++
	}
	for end > start && abs16(a.PCM[end-1]) < thr {
		end--
	}
	margin := a.Rate / 50 // 20 ms
	if start-margin > 0 {
		start -= margin
	} else {
		start = 0
	}
	if end+margin < len(a.PCM) {
		end += margin
	} else {
		end = len(a.PCM)
	}
	a.PCM = a.PCM[start:end]
}

func abs16(v int16) int16 {
	if v < 0 {
		if v == math.MinInt16 {
			return math.MaxInt16
		}
		return -v
	}
	return v
}

// Pad adds silence before and after.
func (a *Audio) Pad(lead, trail time.Duration) {
	if lead <= 0 && trail <= 0 {
		return
	}
	var parts []*Audio
	if lead > 0 {
		parts = append(parts, Silence(a.Rate, lead))
	}
	parts = append(parts, a)
	if trail > 0 {
		parts = append(parts, Silence(a.Rate, trail))
	}
	joined := Concat(parts...)
	a.PCM = joined.PCM
}

// --- encoders -----------------------------------------------------------

// WAV encodes as RIFF/WAVE, 16-bit PCM mono.
func (a *Audio) WAV() []byte {
	data := make([]byte, len(a.PCM)*2)
	for i, s := range a.PCM {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(s))
	}
	return riff(1, a.Rate, 16, data)
}

// WAVMulaw encodes as RIFF/WAVE G.711 µ-law (8-bit); the natural
// telephony container (Asterisk, Twilio, most SIP gateways).
func (a *Audio) WAVMulaw() []byte {
	return riff(7, a.Rate, 8, a.Mulaw())
}

// Mulaw returns raw G.711 µ-law bytes (Asterisk .ulaw / .pcm files).
func (a *Audio) Mulaw() []byte {
	out := make([]byte, len(a.PCM))
	for i, s := range a.PCM {
		out[i] = linear2ulaw(s)
	}
	return out
}

// Alaw returns raw G.711 A-law bytes (Asterisk .alaw files).
func (a *Audio) Alaw() []byte {
	out := make([]byte, len(a.PCM))
	for i, s := range a.PCM {
		out[i] = linear2alaw(s)
	}
	return out
}

// SLN returns raw signed-linear 16-bit little-endian samples (Asterisk
// .sln at 8 kHz, .sln16 at 16 kHz).
func (a *Audio) SLN() []byte {
	data := make([]byte, len(a.PCM)*2)
	for i, s := range a.PCM {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(s))
	}
	return data
}

func riff(format uint16, rate, bits int, data []byte) []byte {
	channels := 1
	blockAlign := channels * bits / 8
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+len(data)))
	b.WriteString("WAVE")
	b.WriteString("fmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, format)
	_ = binary.Write(&b, binary.LittleEndian, uint16(channels))
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate))
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate*blockAlign))
	_ = binary.Write(&b, binary.LittleEndian, uint16(blockAlign))
	_ = binary.Write(&b, binary.LittleEndian, uint16(bits))
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	b.Write(data)
	return b.Bytes()
}

// --- decoders -----------------------------------------------------------

// maxAudioBytes bounds what a decoder accepts from an engine (a stuck
// provider must not balloon memory).
const maxAudioBytes = 32 << 20

// Decode sniffs the container and decodes to mono 16-bit PCM. hint
// describes headerless payloads ("pcm16:24000", "ulaw:8000",
// "alaw:8000") or forces a container ("mp3", "wav"); contentType (may be
// empty) is a weaker hint. Unknown input is sniffed: RIFF → WAV, ID3 tag
// or MPEG frame sync → MP3.
func Decode(data []byte, contentType, hint string) (*Audio, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty audio")
	}
	if len(data) > maxAudioBytes {
		return nil, fmt.Errorf("audio too large (%d bytes)", len(data))
	}
	kind, rate := splitHint(hint)
	switch kind {
	case "pcm16", "pcm", "sln", "sln16", "s16le":
		if rate == 0 {
			rate = 16000
			if kind == "sln" {
				rate = 8000
			}
		}
		return DecodePCM16(data, rate, 1), nil
	case "ulaw", "mulaw", "g711u":
		if rate == 0 {
			rate = 8000
		}
		return decodeG711(data, rate, ulaw2linear), nil
	case "alaw", "g711a":
		if rate == 0 {
			rate = 8000
		}
		return decodeG711(data, rate, alaw2linear), nil
	case "wav":
		return DecodeWAV(data)
	case "mp3":
		return DecodeMP3(data)
	}
	switch {
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE":
		return DecodeWAV(data)
	case len(data) >= 3 && string(data[:3]) == "ID3",
		len(data) >= 2 && data[0] == 0xFF && data[1]&0xE0 == 0xE0:
		return DecodeMP3(data)
	}
	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "mpeg"), strings.Contains(ct, "mp3"):
		return DecodeMP3(data)
	case strings.Contains(ct, "wav"):
		return DecodeWAV(data)
	case strings.Contains(ct, "basic"): // audio/basic = µ-law 8 kHz
		return decodeG711(data, 8000, ulaw2linear), nil
	}
	// Last resort: an MP3 whose frame sync starts after junk.
	if a, err := DecodeMP3(data); err == nil {
		return a, nil
	}
	return nil, fmt.Errorf("unrecognised audio format (content-type %q, hint %q)", contentType, hint)
}

// splitHint parses "kind:rate" / "kind_rate" / "kind@rate".
func splitHint(h string) (kind string, rate int) {
	h = strings.ToLower(strings.TrimSpace(h))
	if i := strings.IndexAny(h, ":_@"); i >= 0 {
		kind = h[:i]
		for _, c := range h[i+1:] {
			if c < '0' || c > '9' {
				break
			}
			rate = rate*10 + int(c-'0')
		}
		return kind, rate
	}
	return h, 0
}

// DecodePCM16 wraps raw little-endian 16-bit samples; stereo is
// down-mixed.
func DecodePCM16(data []byte, rate, channels int) *Audio {
	if channels < 1 {
		channels = 1
	}
	frames := len(data) / (2 * channels)
	out := make([]int16, frames)
	for i := 0; i < frames; i++ {
		var sum int
		for c := 0; c < channels; c++ {
			sum += int(int16(binary.LittleEndian.Uint16(data[(i*channels+c)*2:])))
		}
		out[i] = int16(sum / channels)
	}
	return &Audio{Rate: rate, PCM: out}
}

func decodeG711(data []byte, rate int, fn func(byte) int16) *Audio {
	out := make([]int16, len(data))
	for i, b := range data {
		out[i] = fn(b)
	}
	return &Audio{Rate: rate, PCM: out}
}

// DecodeWAV parses RIFF/WAVE: PCM 8/16/24/32-bit, IEEE float, µ-law,
// A-law, WAVE_FORMAT_EXTENSIBLE; any channel count (down-mixed to mono).
func DecodeWAV(data []byte) (*Audio, error) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, fmt.Errorf("not a RIFF/WAVE file")
	}
	var (
		format, channels, bits int
		rate                   int
		pcm                    []byte
		haveFmt                bool
	)
	pos := 12
	for pos+8 <= len(data) {
		id := string(data[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(data[pos+4:]))
		pos += 8
		end := pos + size
		if end > len(data) || end < pos {
			end = len(data)
		}
		chunk := data[pos:end]
		switch id {
		case "fmt ":
			if len(chunk) < 16 {
				return nil, fmt.Errorf("wav: short fmt chunk")
			}
			format = int(binary.LittleEndian.Uint16(chunk[0:]))
			channels = int(binary.LittleEndian.Uint16(chunk[2:]))
			rate = int(binary.LittleEndian.Uint32(chunk[4:]))
			bits = int(binary.LittleEndian.Uint16(chunk[14:]))
			if format == 0xFFFE && len(chunk) >= 26 { // EXTENSIBLE: sub-format GUID
				format = int(binary.LittleEndian.Uint16(chunk[24:]))
			}
			haveFmt = true
		case "data":
			pcm = chunk
		}
		pos = end
		if size%2 == 1 { // RIFF chunks are word-aligned
			pos++
		}
		if pcm != nil && haveFmt {
			break
		}
	}
	if !haveFmt || pcm == nil {
		return nil, fmt.Errorf("wav: missing fmt/data chunk")
	}
	if channels < 1 || rate <= 0 {
		return nil, fmt.Errorf("wav: bad header (channels=%d rate=%d)", channels, rate)
	}
	switch format {
	case 1: // PCM
		switch bits {
		case 8:
			out := make([]int16, len(pcm)/channels)
			for i := range out {
				var sum int
				for c := 0; c < channels; c++ {
					sum += (int(pcm[i*channels+c]) - 128) << 8
				}
				out[i] = int16(sum / channels)
			}
			return &Audio{Rate: rate, PCM: out}, nil
		case 16:
			return DecodePCM16(pcm, rate, channels), nil
		case 24:
			frames := len(pcm) / (3 * channels)
			out := make([]int16, frames)
			for i := 0; i < frames; i++ {
				var sum int
				for c := 0; c < channels; c++ {
					o := (i*channels + c) * 3
					v := int32(pcm[o]) | int32(pcm[o+1])<<8 | int32(pcm[o+2])<<16
					if v&0x800000 != 0 {
						v |= ^0xFFFFFF
					}
					sum += int(v >> 8)
				}
				out[i] = int16(sum / channels)
			}
			return &Audio{Rate: rate, PCM: out}, nil
		case 32:
			frames := len(pcm) / (4 * channels)
			out := make([]int16, frames)
			for i := 0; i < frames; i++ {
				var sum int
				for c := 0; c < channels; c++ {
					sum += int(int32(binary.LittleEndian.Uint32(pcm[(i*channels+c)*4:])) >> 16)
				}
				out[i] = int16(sum / channels)
			}
			return &Audio{Rate: rate, PCM: out}, nil
		}
	case 3: // IEEE float
		if bits == 32 {
			frames := len(pcm) / (4 * channels)
			out := make([]int16, frames)
			for i := 0; i < frames; i++ {
				var sum float64
				for c := 0; c < channels; c++ {
					sum += float64(math.Float32frombits(binary.LittleEndian.Uint32(pcm[(i*channels+c)*4:])))
				}
				out[i] = clip16(sum / float64(channels) * 32767)
			}
			return &Audio{Rate: rate, PCM: out}, nil
		}
	case 6, 7: // A-law, µ-law
		fn := alaw2linear
		if format == 7 {
			fn = ulaw2linear
		}
		frames := len(pcm) / channels
		out := make([]int16, frames)
		for i := 0; i < frames; i++ {
			var sum int
			for c := 0; c < channels; c++ {
				sum += int(fn(pcm[i*channels+c]))
			}
			out[i] = int16(sum / channels)
		}
		return &Audio{Rate: rate, PCM: out}, nil
	}
	return nil, fmt.Errorf("wav: unsupported format %d/%d-bit", format, bits)
}

// --- G.711 --------------------------------------------------------------

const (
	ulawBias = 0x84
	ulawClip = 32635
)

func linear2ulaw(sample int16) byte {
	s := int(sample)
	sign := 0
	if s < 0 {
		s = -s
		sign = 0x80
	}
	if s > ulawClip {
		s = ulawClip
	}
	s += ulawBias
	exponent := 7
	for mask := 0x4000; exponent > 0 && s&mask == 0; mask >>= 1 {
		exponent--
	}
	mantissa := (s >> (exponent + 3)) & 0x0F
	return ^byte(sign | (exponent << 4) | mantissa)
}

func ulaw2linear(u byte) int16 {
	u = ^u
	sign := int(u & 0x80)
	exponent := int(u>>4) & 0x07
	mantissa := int(u & 0x0F)
	s := ((mantissa << 3) + ulawBias) << exponent
	s -= ulawBias
	if sign != 0 {
		return int16(-s)
	}
	return int16(s)
}

func linear2alaw(sample int16) byte {
	s := int(sample)
	sign := 0x80
	if s < 0 {
		s = -s - 1
		sign = 0
	}
	s >>= 3 // 13-bit
	var out int
	if s < 32 {
		out = s >> 1
	} else {
		exponent := 1
		for v := s >> 5; v > 1 && exponent < 7; v >>= 1 {
			exponent++
		}
		mantissa := (s >> exponent) & 0x0F
		out = (exponent << 4) | mantissa
	}
	return byte((out | sign) ^ 0x55)
}

func alaw2linear(a byte) int16 {
	a ^= 0x55
	sign := a & 0x80
	exponent := int(a>>4) & 0x07
	mantissa := int(a & 0x0F)
	var s int
	if exponent == 0 {
		s = (mantissa << 4) + 8
	} else {
		s = ((mantissa << 4) + 0x108) << (exponent - 1)
	}
	if sign == 0 {
		s = -s
	}
	return int16(s)
}
