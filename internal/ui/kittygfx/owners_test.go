package kittygfx_test

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/stokaro/kc/internal/ui/kittygfx"
)

// TestOwns: two sets on one terminal take only their own transmits back;
// a cell size is for all of them.
func TestOwns(t *testing.T) {
	t.Parallel()
	a := kittygfx.NewImages(kittygfx.Encoder{}, 0xC0, 1)
	b := kittygfx.NewImages(kittygfx.Encoder{}, 0xD0, 1)
	msg := a.Show(0, picture(1), 4, 2)()
	if !a.Owns(msg) || b.Owns(msg) {
		t.Fatalf("a's transmit: a owns it %v, b owns it %v", a.Owns(msg), b.Owns(msg))
	}
	b.Handle(msg)
	if _, ok := b.Shown(0, 4, 2); ok {
		t.Error("b shows a picture a sent")
	}
	a.Handle(msg)
	if id, ok := a.Shown(0, 4, 2); !ok || id != 0xC0 {
		t.Errorf("a shows %d, %v; want image 0xC0", id, ok)
	}
	size := uv.CellSizeEvent{Width: 16, Height: 34}
	if !a.Owns(size) || !b.Owns(size) {
		t.Error("a cell size is not for every set")
	}
	if a.Owns(tea.KeyPressMsg{}) {
		t.Error("a key press is a's")
	}
}

// TestForget: a key no longer shown is deleted alone, a transmit of it on
// its way does not show, and the key draws again afterwards.
func TestForget(t *testing.T) {
	t.Parallel()
	im := kittygfx.NewImages(kittygfx.Encoder{}, 0xC0, 2)
	deliver(t, im, im.Show(0, picture(1), 4, 2))
	deliver(t, im, im.Show(1, picture(1), 4, 2))
	if !im.Holds(0) || !im.Holds(1) {
		t.Fatal("the sent keys are not held")
	}
	late, _ := im.Finish(mustBegin(t, im, 0).Encode(picture(2)))
	out := im.Forget(0)
	if !strings.Contains(out, "a=d") || !strings.Contains(out, "i=192") || strings.Contains(out, "i=194") {
		t.Errorf("Forget(0) wrote %q, want the deletes of key 0 only", out)
	}
	if im.Holds(0) || !im.Holds(1) {
		t.Errorf("after Forget(0): holds 0 %v, holds 1 %v", im.Holds(0), im.Holds(1))
	}
	if im.Forget(0) != "" {
		t.Error("a second Forget deleted again")
	}
	// The transmit of the old picture is written after the delete.
	deliver(t, im, late)
	if _, ok := im.Shown(0, 4, 2); ok {
		t.Error("a transmit of a forgotten key shows")
	}
	if !im.Holds(0) {
		t.Error("the terminal holds the late transmit, but kc does not delete it")
	}
	if !strings.Contains(im.Forget(0), "a=d") {
		t.Error("the late transmit is not deleted")
	}
	deliver(t, im, im.Show(0, picture(3), 4, 2))
	if _, ok := im.Shown(0, 4, 2); !ok {
		t.Error("the key does not draw again after Forget")
	}
}

// TestLateTransmitAfterRelease: a transmit written after a Release is
// deleted by the next one.
func TestLateTransmitAfterRelease(t *testing.T) {
	t.Parallel()
	im := kittygfx.NewImages(kittygfx.Encoder{}, 0xC0, 1)
	late, _ := im.Finish(mustBegin(t, im, 0).Encode(picture(1)))
	if out := im.Release(); !strings.Contains(out, "i=192") {
		t.Fatalf("Release wrote %q, want the delete of the transmit on its way", out)
	}
	deliver(t, im, late)
	if _, ok := im.Shown(0, 4, 2); ok {
		t.Error("a transmit begun before the release shows")
	}
	if out := im.Release(); !strings.Contains(out, "i=192") {
		t.Errorf("the next Release wrote %q, want the late transmit deleted", out)
	}
}

func mustBegin(t *testing.T, im *kittygfx.Images, key int) kittygfx.Job {
	t.Helper()
	job, ok := im.Begin(key, 4, 2)
	if !ok {
		t.Fatalf("Begin(%d) refused", key)
	}
	return job
}
