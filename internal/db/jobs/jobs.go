package jobs

import (
	"github.com/riverqueue/river"
)

// Queue names as constants.
const (
	MaxRetries int = 3

	QueueDefault      string = river.QueueDefault
	MaxDefaultWorkers int    = 20
)

// RegisterWorkers registers all worker types.
func RegisterWorkers(workers *river.Workers) {
	// river.AddWorker(workers, &CustomerWorker{})
	// Add other Helcim workers here
}
