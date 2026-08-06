package kvcache

import (
	"context"
	"encoding/binary"
	"fmt"

	zmq4 "github.com/go-zeromq/zmq4"
)

const replayEndSequence = ^uint64(0)

var _ EventSource = zmqSource{}

// zmqSource 实现 vLLM PUB/SUB 实时流和 ROUTER/DEALER replay 协议。
// handler 是同步调用：上一个事件没有完成 index apply 前，不会继续接收下一个事件。
type zmqSource struct{}

// NewZMQSource 返回无隐藏 goroutine、按调用生命周期创建 socket 的事件 transport。
func NewZMQSource() EventSource {
	return zmqSource{}
}

// Follow 订阅当前实例的精确 topic，并持续同步交付实时事件。
func (zmqSource) Follow(ctx context.Context, instance Instance, handler func(Event) error) error {
	subscriber := zmq4.NewSub(ctx)
	defer subscriber.Close()
	if err := subscriber.Dial(instance.EventsEndpoint); err != nil {
		return fmt.Errorf("dial live endpoint %q: %w", instance.EventsEndpoint, err)
	}
	topic := instanceTopic(instance)
	if err := subscriber.SetOption(zmq4.OptionSubscribe, topic); err != nil {
		return fmt.Errorf("subscribe topic %q: %w", topic, err)
	}

	for {
		message, err := subscriber.Recv()
		if err != nil {
			return fmt.Errorf("receive live event: %w", err)
		}
		event, err := decodeLiveMessage(message.Frames)
		if err != nil {
			return err
		}
		if err := handler(event); err != nil {
			return err
		}
	}
}

// Replay 从指定 sequence 开始流式补偿，只有收到 vLLM END sentinel 后才返回成功。
func (zmqSource) Replay(ctx context.Context, instance Instance, start uint64, handler func(Event) error) error {
	dealer := zmq4.NewDealer(ctx)
	defer dealer.Close()
	if err := dealer.Dial(instance.ReplayEndpoint); err != nil {
		return fmt.Errorf("dial replay endpoint %q: %w", instance.ReplayEndpoint, err)
	}

	sequence := make([]byte, 8)
	binary.BigEndian.PutUint64(sequence, start)
	if err := dealer.SendMulti(zmq4.NewMsgFrom(nil, sequence)); err != nil {
		return fmt.Errorf("send replay request: %w", err)
	}
	topic := instanceTopic(instance)
	for {
		message, err := dealer.Recv()
		if err != nil {
			return fmt.Errorf("receive replay event: %w", err)
		}
		event, end, err := decodeReplayMessage(topic, message.Frames)
		if err != nil {
			return err
		}
		if end {
			return nil
		}
		if err := handler(event); err != nil {
			return err
		}
	}
}

func decodeLiveMessage(frames [][]byte) (Event, error) {
	if len(frames) != 3 || len(frames[1]) != 8 {
		return Event{}, fmt.Errorf("live event must contain topic, 8-byte sequence and payload")
	}
	return Event{
		Topic:    string(frames[0]),
		Sequence: binary.BigEndian.Uint64(frames[1]),
		Payload:  frames[2],
	}, nil
}

func decodeReplayMessage(topic string, frames [][]byte) (Event, bool, error) {
	if len(frames) == 3 && len(frames[0]) == 0 {
		frames = frames[1:]
	}
	if len(frames) != 2 || len(frames[0]) != 8 {
		return Event{}, false, fmt.Errorf("replay event must contain 8-byte sequence and payload")
	}
	sequence := binary.BigEndian.Uint64(frames[0])
	if sequence == replayEndSequence {
		return Event{}, true, nil
	}
	return Event{Topic: topic, Sequence: sequence, Payload: frames[1]}, false, nil
}

func instanceTopic(instance Instance) string {
	return topicPrefix + instance.PodIdentifier + "@" + instance.Model
}
