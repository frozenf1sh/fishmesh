package prediction

import (
	"math"
	"sort"
	"sync"
	"time"

	"github.com/frozenf1sh/fishmesh/internal/serving/backend"
)

const (
	featureCount          = 5 // 常数项、未缓存 token、queue、running、local in-flight。
	coordinateDescentPass = 64
	ridgeRegularization   = 1.0
)

var _ Tracker = (*tracker)(nil)

type sample struct {
	at       time.Time
	features Features
	ttft     time.Duration
}

type tracker struct {
	config Config
	now    func() time.Time

	mu      sync.Mutex
	samples map[backend.ID][]sample
}

type ticketState struct {
	tracker   *tracker
	backend   backend.ID
	features  Features
	predicted time.Duration

	once        sync.Once
	observation Observation
}

// New 创建不依赖 HTTP、routing、KV 或 tokenization 的纯观测域。
func New(config Config) (Tracker, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	now := config.Clock
	if now == nil {
		now = time.Now
	}
	return &tracker{config: config, now: now, samples: make(map[backend.ID][]sample)}, nil
}

func (t *tracker) Begin(input BeginInput) (Ticket, Shadow) {
	if t.config.Mode == ModeOff {
		return Ticket{}, Shadow{Status: StatusDisabled}
	}
	if input.Selected == "" {
		return Ticket{}, Shadow{Status: StatusInsufficientData}
	}
	if !validFeatures(input.Features) {
		return Ticket{}, Shadow{Status: StatusLoadUnavailable}
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.pruneLocked(now)
	shadow := t.shadowLocked(input)
	predicted := time.Duration(0)
	if shadow.Status == StatusAvailable {
		predicted = shadow.SelectedEstimate
	}
	return Ticket{state: &ticketState{tracker: t, backend: input.Selected, features: input.Features, predicted: predicted}}, shadow
}

func (t *tracker) Reconcile(backends []backend.ID) {
	active := make(map[backend.ID]struct{}, len(backends))
	for _, id := range backends {
		active[id] = struct{}{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for id := range t.samples {
		if _, ok := active[id]; !ok {
			delete(t.samples, id)
		}
	}
}

func (t *tracker) shadowLocked(input BeginInput) Shadow {
	if len(input.Candidates) == 0 {
		return Shadow{Status: StatusInsufficientData}
	}
	result := Shadow{Status: StatusAvailable}
	best := time.Duration(0)
	for _, candidate := range input.Candidates {
		if candidate.Backend == "" || !validFeatures(candidate.Features) {
			return Shadow{Status: StatusLoadUnavailable}
		}
		model, ok := fit(t.samples[candidate.Backend], t.config.MinimumSamples)
		if !ok {
			return Shadow{Status: StatusInsufficientData}
		}
		estimate := model.estimate(candidate.Features)
		if candidate.Backend == input.Selected {
			result.SelectedEstimate = estimate
			result.SamplesPerBackend = len(t.samples[candidate.Backend])
		}
		if result.WouldSelect == "" || estimate < best || (estimate == best && candidate.Backend < result.WouldSelect) {
			result.WouldSelect, result.WouldSelectTTFT, best = candidate.Backend, estimate, estimate
		}
	}
	if result.SelectedEstimate <= 0 || result.WouldSelect == "" {
		return Shadow{Status: StatusInsufficientData}
	}
	return result
}

func (t *tracker) record(backendID backend.ID, features Features, ttft time.Duration) {
	if ttft <= 0 || !validFeatures(features) {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.pruneLocked(now)
	samples := append(t.samples[backendID], sample{at: now, features: features, ttft: ttft})
	if len(samples) > t.config.MaxSamples {
		samples = append([]sample(nil), samples[len(samples)-t.config.MaxSamples:]...)
	}
	t.samples[backendID] = samples
}

func (t *tracker) pruneLocked(now time.Time) {
	cutoff := now.Add(-t.config.MaxSampleAge)
	for id, samples := range t.samples {
		first := sort.Search(len(samples), func(index int) bool { return !samples[index].at.Before(cutoff) })
		if first == len(samples) {
			delete(t.samples, id)
			continue
		}
		if first > 0 {
			t.samples[id] = append([]sample(nil), samples[first:]...)
		}
	}
}

func (s *ticketState) observe(ttft time.Duration) Observation {
	s.once.Do(func() {
		s.tracker.record(s.backend, s.features, ttft)
		if s.predicted <= 0 || ttft <= 0 {
			return
		}
		s.observation = Observation{Valid: true, Backend: s.backend, Predicted: s.predicted, Actual: ttft, Error: ttft - s.predicted}
	})
	return s.observation
}

func validFeatures(features Features) bool {
	return features.LoadValid && features.UncachedTokens >= 0 && features.QueueDepth >= 0 && features.Running >= 0 && features.LocalInflight >= 0
}

type model struct{ coefficients [featureCount]float64 }

func fit(samples []sample, minimum int) (model, bool) {
	if len(samples) < minimum {
		return model{}, false
	}
	var normal [featureCount][featureCount]float64
	var target [featureCount]float64
	for _, value := range samples {
		if value.ttft <= 0 || !validFeatures(value.features) {
			continue
		}
		x := featureVector(value.features)
		y := float64(value.ttft) / float64(time.Millisecond)
		for row := range featureCount {
			target[row] += x[row] * y
			for column := range featureCount {
				normal[row][column] += x[row] * x[column]
			}
		}
	}
	for index := 1; index < featureCount; index++ {
		normal[index][index] += ridgeRegularization
	}
	var coefficients [featureCount]float64
	for range coordinateDescentPass {
		for row := range featureCount {
			if normal[row][row] == 0 {
				continue
			}
			residual := target[row]
			for column := range featureCount {
				if column != row {
					residual -= normal[row][column] * coefficients[column]
				}
			}
			coefficients[row] = math.Max(0, residual/normal[row][row])
		}
	}
	return model{coefficients: coefficients}, true
}

func featureVector(features Features) [featureCount]float64 {
	return [featureCount]float64{1, float64(features.UncachedTokens), float64(features.QueueDepth), float64(features.Running), float64(features.LocalInflight)}
}

func (m model) estimate(features Features) time.Duration {
	x := featureVector(features)
	var milliseconds float64
	for index := range featureCount {
		milliseconds += m.coefficients[index] * x[index]
	}
	if milliseconds <= 0 || milliseconds >= float64(math.MaxInt64)/float64(time.Millisecond) {
		return 0
	}
	return time.Duration(milliseconds * float64(time.Millisecond))
}
