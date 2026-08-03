package simulator

import (
	"fmt"
	"net/http"
)

func (b *Backend) serveMetrics(writer http.ResponseWriter, _ *http.Request) {
	state := b.Snapshot()
	writer.Header().Set(headerContentType, mediaTypeText)
	_, _ = fmt.Fprintf(writer,
		"# TYPE %s gauge\n%s %g\n# TYPE %s gauge\n%s %g\n",
		metricRequestsWaiting, metricRequestsWaiting, state.Behavior.QueueDepth,
		metricRequestsRunning, metricRequestsRunning, state.Behavior.RunningRequests,
	)
}
