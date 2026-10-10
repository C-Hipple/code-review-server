package main

import (
	"errors"
	"io"
	"log"
	"sync"
	"time"
)

// frameWriter is the only thing that writes to Chrome. Responses come from
// the server's stdout pump and host events from run; funnelling both through
// one goroutine keeps every frame whole and every chunk stream contiguous,
// since a batch passed to send is written with nothing in between.
type frameWriter struct {
	batches chan [][]byte
	quit    chan struct{}
	done    chan struct{}
	once    sync.Once
	log     *log.Logger
}

func newFrameWriter(w io.Writer, logger *log.Logger) *frameWriter {
	fw := &frameWriter{
		batches: make(chan [][]byte, 16),
		quit:    make(chan struct{}),
		done:    make(chan struct{}),
		log:     logger,
	}
	go fw.loop(w)
	return fw
}

func (fw *frameWriter) loop(w io.Writer) {
	defer close(fw.done)
	broken := false
	write := func(frames [][]byte) {
		for _, frame := range frames {
			// Once Chrome stops reading, the rest are dropped rather than
			// left to block the pump; stdin's EOF ends the host shortly.
			if broken {
				return
			}
			if err := writeFrame(w, frame); err != nil {
				fw.log.Printf("writing to Chrome: %v", err)
				// An oversized frame was refused before anything was
				// written, so the stream is still intact.
				broken = !errors.Is(err, errFrameTooLarge)
			}
		}
	}
	for {
		select {
		case frames := <-fw.batches:
			write(frames)
		case <-fw.quit:
			for {
				select {
				case frames := <-fw.batches:
					write(frames)
				default:
					return
				}
			}
		}
	}
}

// send queues frames to be written back to back. It reports false when the
// writer has already shut down and the frames were dropped.
func (fw *frameWriter) send(frames ...[]byte) bool {
	select {
	case fw.batches <- frames:
		return true
	case <-fw.done:
		return false
	}
}

func (fw *frameWriter) sendEvent(ev hostEvent) {
	msg, err := encodeMessage(hostMessage{Host: ev})
	if err != nil {
		fw.log.Printf("encoding %s event: %v", ev.Event, err)
		return
	}
	fw.send(msg)
}

// close writes whatever is queued and stops the writer, giving up after
// timeout so a Chrome that has stopped reading can't keep the host alive.
func (fw *frameWriter) close(timeout time.Duration) {
	fw.once.Do(func() { close(fw.quit) })
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-fw.done:
	case <-timer.C:
		fw.log.Printf("gave up delivering queued messages to Chrome after %v", timeout)
	}
}
