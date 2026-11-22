// Package logview turns log lines into display lines for the logs viewer.
//
// It keeps every original line as a Record, detects JSON objects, and
// renders each record either raw or formatted (time, level, message, then the
// other fields as key=value). A Query filters records and can extract fields
// with a dotted path such as .msg or .http.status. Filters and modes change
// only the display: the records stay as they arrived, so switching back is
// instant and needs no new stream.
//
// The package has no UI dependencies; the viewer feeds it lines and shows the
// strings it returns.
package logview
