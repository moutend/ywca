// +build windows
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/go-ole/go-ole"
	"github.com/moutend/go-wca"
)

type WAVEFormat struct {
	FormatTag      uint16
	Channels       uint16
	SamplesPerSec  uint32
	AvgBytesPerSec uint32
	BlockAlign     uint16
	BitsPerSample  uint16
	DataSize       uint32
	RawData        []byte
}

func (v *WAVEFormat) Bytes() (output []byte) {
	buf := new(bytes.Buffer)

	binary.Write(buf, binary.BigEndian, []byte("RIFF"))
	binary.Write(buf, binary.LittleEndian, uint32(v.DataSize+36)) // Header size is 44 byte, so 44 - 8 = 36
	binary.Write(buf, binary.BigEndian, []byte("WAVEfmt "))
	binary.Write(buf, binary.LittleEndian, uint32(16)) // 16 (0x10000000) for PCM
	binary.Write(buf, binary.LittleEndian, uint16(1))  // 1 (0x0001) for PCM
	binary.Write(buf, binary.LittleEndian, v.Channels)
	binary.Write(buf, binary.LittleEndian, v.SamplesPerSec)
	binary.Write(buf, binary.LittleEndian, v.AvgBytesPerSec)
	binary.Write(buf, binary.LittleEndian, v.BlockAlign)
	binary.Write(buf, binary.LittleEndian, v.BitsPerSample)
	binary.Write(buf, binary.BigEndian, []byte("data"))
	binary.Write(buf, binary.LittleEndian, v.DataSize)
	binary.Write(buf, binary.LittleEndian, v.RawData)

	return buf.Bytes()
}

type DurationFlag struct {
	Value time.Duration
}

func (f *DurationFlag) Set(value string) (err error) {
	var sec float64

	if sec, err = strconv.ParseFloat(value, 64); err != nil {
		return
	}
	f.Value = time.Duration(sec * float64(time.Second))
	return
}

func (f *DurationFlag) String() string {
	return f.Value.String()
}

type FilenameFlag struct {
	Value string
}

func (f *FilenameFlag) Set(value string) (err error) {
	if !strings.HasSuffix(value, ".wav") {
		err = fmt.Errorf("specify WAVE audio file (*.wav)")
		return
	}
	f.Value = value
	return
}

func (f *FilenameFlag) String() string {
	return f.Value
}

func main() {
	var err error
	if err = run(os.Args); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) (err error) {
	var durationFlag DurationFlag
	var filenameFlag FilenameFlag
	var audio *WAVEFormat

	f := flag.NewFlagSet(args[0], flag.ExitOnError)
	f.Var(&durationFlag, "duration", "Specify recording duration in second")
	f.Var(&durationFlag, "d", "Alias of --duration")
	f.Var(&filenameFlag, "output", "file name")
	f.Var(&filenameFlag, "o", "Alias of --output")
	f.Parse(args[1:])

	if filenameFlag.Value == "" {
		return
	}
	if audio, err = loopbackCaptureSharedTimerDriven(durationFlag.Value); err != nil {
		return
	}
	if err = ioutil.WriteFile(filenameFlag.Value, audio.Bytes(), 0644); err != nil {
		return
	}
	fmt.Println("Successfully done")
	return
}

func loopbackCaptureSharedTimerDriven(duration time.Duration) (audio *WAVEFormat, err error) {
	if err = ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		return
	}

	var de *wca.IMMDeviceEnumerator
	if err = wca.CoCreateInstance(wca.CLSID_MMDeviceEnumerator, 0, wca.CLSCTX_ALL, wca.IID_IMMDeviceEnumerator, &de); err != nil {
		return
	}
	defer de.Release()

	var mmd *wca.IMMDevice
	if err = de.GetDefaultAudioEndpoint(wca.ERender, wca.EConsole, &mmd); err != nil {
		return
	}
	defer mmd.Release()

	var ps *wca.IPropertyStore
	if err = mmd.OpenPropertyStore(wca.STGM_READ, &ps); err != nil {
		return
	}
	defer ps.Release()

	var pv wca.PROPVARIANT
	if err = ps.GetValue(&wca.PKEY_Device_FriendlyName, &pv); err != nil {
		return
	}
	fmt.Printf("Capturing what you hear from: %s\n", pv.String())

	var ac *wca.IAudioClient
	if err = mmd.Activate(wca.IID_IAudioClient, wca.CLSCTX_ALL, nil, &ac); err != nil {
		return
	}
	defer ac.Release()

	var wfx *wca.WAVEFORMATEX
	if err = ac.GetMixFormat(&wfx); err != nil {
		return
	}
	defer ole.CoTaskMemFree(uintptr(unsafe.Pointer(wfx)))

	wfx.WFormatTag = 1
	wfx.NChannels = 2
	wfx.NSamplesPerSec = 44100
	wfx.WBitsPerSample = 16
	wfx.NBlockAlign = (wfx.WBitsPerSample / 8) * wfx.NChannels // 16 bit stereo is 32bit (4 byte) per sample
	wfx.NAvgBytesPerSec = wfx.NSamplesPerSec * uint32(wfx.NBlockAlign)
	wfx.CbSize = 0

	audio = &WAVEFormat{}
	audio.Channels = wfx.NChannels
	audio.SamplesPerSec = wfx.NSamplesPerSec
	audio.AvgBytesPerSec = wfx.NAvgBytesPerSec
	audio.BlockAlign = wfx.NBlockAlign
	audio.BitsPerSample = wfx.WBitsPerSample

	fmt.Println("--------")
	fmt.Printf("Format: PCM %d bit signed integer\n", wfx.WBitsPerSample)
	fmt.Printf("Rate: %d Hz\n", wfx.NSamplesPerSec)
	fmt.Printf("Channels: %d\n", wfx.NChannels)
	fmt.Println("--------")

	var defaultPeriod int64
	var minimumPeriod int64
	var capturingPeriod time.Duration
	if err = ac.GetDevicePeriod(&defaultPeriod, &minimumPeriod); err != nil {
		return
	}
	capturingPeriod = time.Duration(int(defaultPeriod) * 100)
	fmt.Printf("Default capturing period: %d ms\n", capturingPeriod/time.Millisecond)

	if err = ac.Initialize(wca.AUDCLNT_SHAREMODE_SHARED, wca.AUDCLNT_STREAMFLAGS_LOOPBACK, 200*10000, 0, wfx, nil); err != nil {
		return
	}

	var bufferFrameSize uint32
	if err = ac.GetBufferSize(&bufferFrameSize); err != nil {
		return
	}
	fmt.Printf("Allocated buffer size: %d\n", bufferFrameSize)

	var acc *wca.IAudioCaptureClient
	if err = ac.GetService(wca.IID_IAudioCaptureClient, &acc); err != nil {
		return
	}
	defer acc.Release()

	if err = ac.Start(); err != nil {
		return
	}
	fmt.Println("Start capturing loopback audio with shared-timer-driven mode")
	if duration <= 0 {
		fmt.Println("Press Ctrl-C to stop capturing")
	}
	time.Sleep(capturingPeriod)

	var isCapturing bool = true
	var currentDuration time.Duration
	var data *byte
	var b *byte
	var availableFrameSize uint32
	var flags uint32
	var devicePosition uint64
	var qcpPosition uint64
	var padding uint32

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt)

	for {
		if !isCapturing {
			break
		}
		select {
		case <-signalChan:
			fmt.Println("Interrupted by SIGINT")
			isCapturing = false
			break
		default:
			currentDuration = time.Duration(float64(audio.DataSize) / float64(audio.BitsPerSample/8) / float64(audio.Channels) / float64(audio.SamplesPerSec) * float64(time.Second))
			if duration != 0 && currentDuration > duration {
				isCapturing = false
				break
			}
			if err = acc.GetBuffer(&data, &availableFrameSize, &flags, &devicePosition, &qcpPosition); err != nil {
				return
			}
			if availableFrameSize == 0 {
				continue
			}

			start := unsafe.Pointer(data)
			lim := int(availableFrameSize) * int(wfx.NBlockAlign)

			for n := 0; n < lim; n++ {
				b = (*byte)(unsafe.Pointer(uintptr(start) + uintptr(n)))
				audio.RawData = append(audio.RawData, *b)
			}
			audio.DataSize += uint32(lim)
			if err = ac.GetCurrentPadding(&padding); err != nil {
				return
			}
			//capturingPeriod = time.Duration(1000000 * 1000 * int(bufferFrameSize-padding) / int(wfx.NSamplesPerSec))
			capturingPeriod = time.Duration(float64(bufferFrameSize-padding) / float64(wfx.NSamplesPerSec) * float64(time.Second))
			time.Sleep(capturingPeriod / 2)
			if err = acc.ReleaseBuffer(availableFrameSize); err != nil {
				return
			}
		}
	}

	fmt.Println("Stop capturing")
	if err = ac.Stop(); err != nil {
		return
	}
	return
}
