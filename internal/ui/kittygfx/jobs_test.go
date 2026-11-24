package kittygfx_test

import (
	"testing"

	"github.com/stokaro/kc/internal/ui/kittygfx"
)

// TestJobsOneInFlightAndStaleAfterRelease: a key has one job at a time,
// a refused Begin is reported by Finish, and a job begun before a
// Release sends nothing.
func TestJobsOneInFlightAndStaleAfterRelease(t *testing.T) {
	t.Parallel()
	im := kittygfx.NewImages(kittygfx.Encoder{}, 240, 2)
	job, ok := im.Begin(0, 4, 2)
	if !ok {
		t.Fatal("first Begin refused")
	}
	if _, ok := im.Begin(0, 4, 2); ok {
		t.Fatal("a second job of the same key began while one is in flight")
	}
	if _, ok := im.Begin(1, 4, 2); !ok {
		t.Fatal("a job of another key was refused")
	}
	if w, h := job.CellSize(); w <= 0 || h <= 0 {
		t.Fatalf("cell size %dx%d", w, h)
	}
	cmd, again := im.Finish(job.Encode(picture(1)))
	if cmd == nil || !again {
		t.Fatalf("Finish: command %v, again %v; want a transmit and again", cmd != nil, again)
	}
	deliver(t, im, cmd)

	stale, _ := im.Begin(0, 4, 2)
	im.Release()
	if cmd, _ := im.Finish(stale.Encode(picture(2))); cmd != nil {
		t.Error("a job begun before the release sent a picture")
	}
	if _, ok := im.Begin(0, 4, 2); !ok {
		t.Error("the key stays busy after its stale job finished")
	}
}

// TestJobOfAHeldPictureSendsNothing: pixels the terminal holds are not
// compressed or sent again; the screen switches to them.
func TestJobOfAHeldPictureSendsNothing(t *testing.T) {
	t.Parallel()
	im := kittygfx.NewImages(kittygfx.Encoder{}, 240, 1)
	deliver(t, im, im.Show(0, picture(1), 4, 2))
	deliver(t, im, im.Show(0, picture(2), 4, 2))
	job, _ := im.Begin(0, 4, 2)
	if cmd, _ := im.Finish(job.Encode(picture(1))); cmd != nil {
		t.Error("held pixels were sent again")
	}
	if got := shownID(t, im, 0, 4, 2); got != kittygfx.Color(240) {
		t.Errorf("shown %q, want image 240 back", got)
	}
}
