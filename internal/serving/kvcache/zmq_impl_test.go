package kvcache

import (
	"encoding/binary"
	"testing"
)

func TestDecodeZMQFrames(t *testing.T) {
	sequence := make([]byte, 8)
	binary.BigEndian.PutUint64(sequence, 42)
	event, err := decodeLiveMessage([][]byte{[]byte("kv@pod@model"), sequence, []byte{1, 2}})
	if err != nil || event.Sequence != 42 || event.Topic != "kv@pod@model" {
		t.Fatalf("decode live frames: event=%+v err=%v", event, err)
	}

	replayed, end, err := decodeReplayMessage("kv@pod@model", [][]byte{nil, sequence, []byte{3}})
	if err != nil || end || replayed.Sequence != 42 {
		t.Fatalf("decode replay frames: event=%+v end=%v err=%v", replayed, end, err)
	}

	binary.BigEndian.PutUint64(sequence, replayEndSequence)
	_, end, err = decodeReplayMessage("kv@pod@model", [][]byte{sequence, nil})
	if err != nil || !end {
		t.Fatalf("decode replay END: end=%v err=%v", end, err)
	}
}
