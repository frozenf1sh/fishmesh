package simulator

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type behaviorDTO struct {
	StatusCode        int     `json:"status_code"`
	FirstEventDelayMS int64   `json:"first_event_delay_ms"`
	EventIntervalMS   int64   `json:"event_interval_ms"`
	Events            int     `json:"events"`
	AbortAfterEvents  int     `json:"abort_after_events"`
	Hold              bool    `json:"hold"`
	QueueDepth        float64 `json:"queue_depth"`
	RunningRequests   float64 `json:"running_requests"`
}

type stateDTO struct {
	Behavior      behaviorDTO `json:"behavior"`
	Requests      int64       `json:"requests"`
	Active        int64       `json:"active"`
	Cancellations int64       `json:"cancellations"`
	ForcedErrors  int64       `json:"forced_errors"`
	StreamAborts  int64       `json:"stream_aborts"`
}

func (b *Backend) updateBehavior(writer http.ResponseWriter, request *http.Request) {
	var payload behaviorDTO
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxControlBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		http.Error(writer, fmt.Sprintf("decode behavior: %v", err), http.StatusBadRequest)
		return
	}
	if err := b.SetBehavior(payload.behavior()); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	b.serveState(writer, request)
}

func (b *Backend) serveState(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set(headerContentType, mediaTypeJSON)
	if err := json.NewEncoder(writer).Encode(stateResponse(b.Snapshot())); err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

func (b behaviorDTO) behavior() Behavior {
	return Behavior{
		StatusCode: b.StatusCode, FirstEventDelay: time.Duration(b.FirstEventDelayMS) * time.Millisecond,
		EventInterval: time.Duration(b.EventIntervalMS) * time.Millisecond, Events: b.Events,
		AbortAfterEvents: b.AbortAfterEvents, Hold: b.Hold, QueueDepth: b.QueueDepth, RunningRequests: b.RunningRequests,
	}
}

func behaviorResponse(behavior Behavior) behaviorDTO {
	return behaviorDTO{
		StatusCode: behavior.StatusCode, FirstEventDelayMS: behavior.FirstEventDelay.Milliseconds(),
		EventIntervalMS: behavior.EventInterval.Milliseconds(), Events: behavior.Events,
		AbortAfterEvents: behavior.AbortAfterEvents, Hold: behavior.Hold,
		QueueDepth: behavior.QueueDepth, RunningRequests: behavior.RunningRequests,
	}
}

func stateResponse(state State) stateDTO {
	return stateDTO{
		Behavior: behaviorResponse(state.Behavior), Requests: state.Requests, Active: state.Active,
		Cancellations: state.Cancellations, ForcedErrors: state.ForcedErrors, StreamAborts: state.StreamAborts,
	}
}
