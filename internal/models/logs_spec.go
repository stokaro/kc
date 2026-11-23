package models

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DefaultLogsTailLines controls how many lines to fetch before following.
const DefaultLogsTailLines int64 = 200

// LogsSpec describes a container log stream.
type LogsSpec struct {
	Namespace string
	Pod       string
	Container string
	Follow    bool
	// TailLines is the number of recent lines to fetch. Zero means
	// DefaultLogsTailLines; a negative value fetches the whole log.
	TailLines int64
	// SinceSeconds limits the log to lines newer than this many seconds.
	// Zero means no limit.
	SinceSeconds int64
	// SinceTime limits the log to lines from this time on. It wins over
	// SinceSeconds; the zero time means no limit. An investigation window
	// sets it.
	SinceTime time.Time
	// Timestamps prefixes every line with its RFC3339 timestamp.
	Timestamps bool
	// Previous reads the log of the previous, terminated instance of the
	// container.
	Previous bool
}

// EffectiveTailLines returns the tail the API request uses: the default for
// zero, and -1 (the whole log) for any negative value.
func (s LogsSpec) EffectiveTailLines() int64 {
	switch {
	case s.TailLines == 0:
		return DefaultLogsTailLines
	case s.TailLines < 0:
		return -1
	default:
		return s.TailLines
	}
}

// PodLogOptions returns the API options for the stream. logsCommand prints
// the kubectl command for the same options; keep the two in step.
func (s LogsSpec) PodLogOptions() *corev1.PodLogOptions {
	opts := &corev1.PodLogOptions{
		Container:  s.Container,
		Follow:     s.Follow,
		Previous:   s.Previous,
		Timestamps: s.Timestamps,
	}
	if tail := s.EffectiveTailLines(); tail > 0 {
		opts.TailLines = &tail
	}
	switch {
	case !s.SinceTime.IsZero():
		since := metav1.NewTime(s.SinceTime)
		opts.SinceTime = &since
	case s.SinceSeconds > 0:
		since := s.SinceSeconds
		opts.SinceSeconds = &since
	}
	return opts
}

// FormatLogsSince prints a since window the way kubectl --since accepts it:
// "1h", "15m", "90s". Zero or less prints an empty string.
func FormatLogsSince(seconds int64) string {
	if seconds <= 0 {
		return ""
	}
	d := time.Duration(seconds) * time.Second
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

// LogsCommander builds the kubectl command that prints the log a spec
// streams. The logs viewer uses it to keep its command line in step with the
// options the user picks.
type LogsCommander interface {
	LogsCommand(spec LogsSpec) string
}
