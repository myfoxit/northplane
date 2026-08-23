package tts

import (
	"bytes"
	"fmt"
	"io"

	mp3 "github.com/hajimehoshi/go-mp3"
)

// DecodeMP3 decodes an MPEG-1/2 Layer III stream (what Edge TTS, OpenAI
// and ElevenLabs deliver by default) to mono PCM with a pure-Go decoder —
// the distroless runtime image has no ffmpeg/sox, and the audio path
// must not depend on host tools.
func DecodeMP3(data []byte) (*Audio, error) {
	dec, err := mp3.NewDecoder(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("mp3: %w", err)
	}
	// go-mp3 always yields 16-bit little-endian stereo frames.
	pcm, err := io.ReadAll(io.LimitReader(dec, maxAudioBytes*4))
	if err != nil && len(pcm) == 0 {
		return nil, fmt.Errorf("mp3: %w", err)
	}
	if len(pcm) == 0 {
		return nil, fmt.Errorf("mp3: no audio frames")
	}
	return DecodePCM16(pcm, dec.SampleRate(), 2), nil
}
