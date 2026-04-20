package record

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"time"

	aac "github.com/gen2brain/aac-go"

	mp4aac "github.com/Eyevinn/mp4ff/aac"
	"github.com/Eyevinn/mp4ff/mp4"

	"root-firmware/pkg/events"
	"root-firmware/pkg/globals"
)

// muxJob represents a recording that needs to be muxed to MP4/M4A and saved
type muxJob struct {
	videoEntries   []lookbackEntry
	audioEntries   []lookbackEntry
	outputPath     string
	duration       float64
	eventID        string
	eventType      string
	preview        []byte
	videoDetection *events.VideoDetectionResult
	audioDetection *events.AudioDetectionResult
}

// muxWorker processes mux jobs serially to avoid CPU contention on the Pi
func (r *Recorder) muxWorker() {
	for job := range r.muxQueue {
		if err := muxVideo(job.videoEntries, job.outputPath, job.duration); err != nil {
			log.Printf("Recorder: Skipping save for %s due to mux failure", job.outputPath)
			continue
		}
		muxAudio(job.audioEntries, job.videoEntries[0].timestamp, job.outputPath)
		if err := events.Get().SaveRecording(job.eventID, job.outputPath, job.duration, job.eventType, job.preview, job.videoDetection, job.audioDetection); err != nil {
			log.Printf("Recorder: Failed to save recording %s: %v", job.eventID, err)
		}
	}
}

// muxVideo writes H.264 GOPs to a fragmented MP4 file
func muxVideo(entries []lookbackEntry, outputPath string, duration float64) error {
	if len(entries) == 0 {
		return fmt.Errorf("no video entries")
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", outputPath, err)
	}
	defer f.Close()

	muxer := newFMP4Muxer(f)
	totalBytes := 0
	for _, e := range entries {
		totalBytes += len(e.data)
		if _, err := muxer.Write(e.data); err != nil {
			log.Printf("Recorder: Failed to mux video: %v", err)
			return err
		}
	}
	if err := muxer.Flush(); err != nil {
		return err
	}

	log.Printf("Recorder: Muxed %d GOPs (%.2fs, %dKB) to %s", len(entries), duration, totalBytes/1024, outputPath)
	return nil
}

// muxAudio encodes PCM audio entries to AAC and wraps in an M4A container
func muxAudio(entries []lookbackEntry, videoStart time.Time, outputPath string) {
	if len(entries) == 0 {
		return
	}

	// Audio and video chunks overlap differently before their timestamps,
	// so align the first audio chunk's start to the video's effective start
	gopInterval := time.Second * time.Duration(globals.CameraGOPSize) / time.Duration(globals.CameraFramerate)
	videoActualStart := videoStart.Add(-gopInterval)
	bytesPerSec := float64(globals.AudioSampleRate * 2)

	var pcm bytes.Buffer
	var prev lookbackEntry
	for _, e := range entries {
		if e.timestamp.Before(videoStart) {
			prev = e
			continue
		}
		if pcm.Len() == 0 {
			chunkDur := time.Duration(len(e.data)) * time.Second / time.Duration(globals.AudioSampleRate*2)
			audioStart := e.timestamp.Add(-chunkDur)
			lead := videoActualStart.Sub(audioStart)
			if lead > 0 {
				// Audio starts before video — trim leading bytes
				trimBytes := int(lead.Seconds()*bytesPerSec) &^ 1
				if trimBytes < len(e.data) {
					pcm.Write(e.data[trimBytes:])
					continue
				}
			} else if lead < 0 && len(prev.data) > 0 {
				// Audio starts after video — include tail of previous chunk
				tailBytes := int((-lead).Seconds()*bytesPerSec) &^ 1
				if tailBytes > 0 && tailBytes <= len(prev.data) {
					pcm.Write(prev.data[len(prev.data)-tailBytes:])
				}
			}
		}
		pcm.Write(e.data)
	}
	if pcm.Len() == 0 {
		return
	}
	pcmSize := pcm.Len()

	// Encode PCM to ADTS AAC
	var adtsBuf bytes.Buffer
	enc, err := aac.NewEncoder(&adtsBuf, &aac.Options{
		SampleRate:  globals.AudioSampleRate,
		NumChannels: 1,
	})
	if err != nil {
		log.Printf("Recorder: Failed to create AAC encoder: %v", err)
		return
	}
	if err := enc.Encode(&pcm); err != nil {
		log.Printf("Recorder: Failed to encode AAC: %v", err)
		return
	}
	enc.Close()

	// Parse ADTS frames into raw AAC payloads
	adtsData := adtsBuf.Bytes()
	var frames [][]byte
	for len(adtsData) > 7 {
		hdr, offset, err := mp4aac.DecodeADTSHeader(bytes.NewReader(adtsData))
		if err != nil {
			break
		}
		adtsData = adtsData[offset:] // Skip any bytes before sync word
		frameLen := int(hdr.HeaderLength) + int(hdr.PayloadLength)
		if frameLen > len(adtsData) {
			break
		}
		frames = append(frames, adtsData[hdr.HeaderLength:frameLen])
		adtsData = adtsData[frameLen:]
	}

	if len(frames) == 0 {
		log.Printf("Recorder: No AAC frames produced")
		return
	}

	// Write M4A (fragmented MP4 with AAC audio track)
	audioPath := outputPath[:len(outputPath)-4] + "_audio.m4a"
	f, err := os.Create(audioPath)
	if err != nil {
		log.Printf("Recorder: Failed to create %s: %v", audioPath, err)
		return
	}
	defer f.Close()

	timescale := uint32(globals.AudioSampleRate)
	samplesPerFrame := uint32(1024) // Standard AAC frame = 1024 samples

	// Build AAC descriptor (SetAACDescriptor hardcodes stereo)
	asc := &mp4aac.AudioSpecificConfig{
		ObjectType:           mp4aac.AAClc,
		ChannelConfiguration: globals.AudioChannels,
		SamplingFrequency:    globals.AudioSampleRate,
	}
	var ascBuf bytes.Buffer
	if err := asc.Encode(&ascBuf); err != nil {
		log.Printf("Recorder: Failed to encode AAC config: %v", err)
		return
	}

	init := mp4.CreateEmptyInit()
	trak := init.AddEmptyTrack(timescale, "audio", "en")
	esds := mp4.CreateEsdsBox(ascBuf.Bytes())
	mp4a := mp4.CreateAudioSampleEntryBox("mp4a", uint16(globals.AudioChannels), uint16(globals.AudioBitsPerSample), uint16(globals.AudioSampleRate), esds)
	trak.Mdia.Minf.Stbl.Stsd.AddChild(mp4a)
	if err := init.Encode(f); err != nil {
		log.Printf("Recorder: Failed to write M4A init: %v", err)
		return
	}

	// Write all frames in a single fragment
	frag, err := mp4.CreateFragment(1, 1)
	if err != nil {
		log.Printf("Recorder: Failed to create M4A fragment: %v", err)
		return
	}
	var decodeTime uint64
	for _, frame := range frames {
		frag.AddFullSample(mp4.FullSample{
			Sample:     mp4.NewSample(mp4.SyncSampleFlags, samplesPerFrame, uint32(len(frame)), 0),
			DecodeTime: decodeTime,
			Data:       frame,
		})
		decodeTime += uint64(samplesPerFrame)
	}
	if err := frag.Encode(f); err != nil {
		log.Printf("Recorder: Failed to write M4A fragment: %v", err)
		return
	}

	log.Printf("Recorder: Muxed audio (%d frames, %dKB PCM) to %s", len(frames), pcmSize/1024, audioPath)
}
