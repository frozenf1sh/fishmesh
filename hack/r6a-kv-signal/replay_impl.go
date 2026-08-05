package main

import (
	"context"
	"encoding/binary"
	"fmt"

	zmq4 "github.com/go-zeromq/zmq4"
)

const replayEndSequence = ^uint64(0)

type rawEvent struct {
	topic    string
	sequence uint64
	payload  []byte
}

func requestReplay(
	ctx context.Context,
	endpoint string,
	topic string,
	startSequence uint64,
) ([]rawEvent, error) {
	// vLLM 使用 ROUTER 并允许一次请求流式返回多个 batch。DEALER 必须显式发送空 delimiter，
	// 不能使用只允许一问一答的 REQ socket。
	dealer := zmq4.NewDealer(ctx)
	defer dealer.Close()
	if err := dealer.Dial(endpoint); err != nil {
		return nil, fmt.Errorf("连接 replay endpoint %s: %w", endpoint, err)
	}

	sequenceBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(sequenceBytes, startSequence)
	if err := dealer.SendMulti(zmq4.NewMsgFrom(nil, sequenceBytes)); err != nil {
		return nil, fmt.Errorf("发送 replay 请求: %w", err)
	}

	events := make([]rawEvent, 0)
	for {
		message, err := dealer.Recv()
		if err != nil {
			return nil, fmt.Errorf("接收 replay 响应: %w", err)
		}
		frames := message.Frames
		if len(frames) == 3 && len(frames[0]) == 0 {
			frames = frames[1:]
		}
		if len(frames) != 2 || len(frames[0]) < 8 {
			continue
		}
		sequence := binary.BigEndian.Uint64(frames[0])
		if sequence == replayEndSequence {
			return events, nil
		}
		events = append(events, rawEvent{topic: topic, sequence: sequence, payload: frames[1]})
	}
}
