//go:build opus

package voicecodec

import (
	"sync"
	"testing"
)

func TestCodecConcurrentUseAndClose(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	packet, err := c.Encode(make([]int16, FrameSize))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				_, _ = c.Encode(make([]int16, FrameSize))
				_, _ = c.Decode(packet)
				_, _ = c.Conceal(make([]int16, FrameSize))
			}
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); c.Close() }()
	wg.Wait()
	c.Close()
	if _, err := c.Encode(make([]int16, FrameSize)); err == nil {
		t.Fatal("encode accepted closed codec")
	}
	if _, err := c.Decode(packet); err == nil {
		t.Fatal("decode accepted closed codec")
	}
	if _, err := c.Conceal(make([]int16, FrameSize)); err == nil {
		t.Fatal("conceal accepted closed codec")
	}
}
