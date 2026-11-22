package podfs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"k8s.io/client-go/tools/remotecommand"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

// ErrTarMissing means the container has no tar, which a copy into it
// needs, as kubectl cp does.
var ErrTarMissing = errors.New("the container has no tar; copying into it needs tar in the image, as kubectl cp does")

// UploadCompleteMarker is the last member of an upload archive, next to
// the copied entry. The container side moves the entry into place only
// when the marker arrived, so an archive cut off between two members (a
// canceled copy) is never taken for a complete one.
const UploadCompleteMarker = ".kc-upload-complete"

// ExtractRequest describes one top-level entry copied into a container.
type ExtractRequest struct {
	// Dir is the absolute container directory the entry goes into.
	Dir string
	// Name is the entry, a single path element. The archive holds Name (a
	// file, or a directory with its tree) and UploadCompleteMarker.
	Name string
	// Replace allows an existing Name: a file is replaced, a directory is
	// merged. Without it an existing Name fails the copy.
	Replace bool
}

// TarExtractor writes an archive into a container directory.
type TarExtractor interface {
	ExtractTar(ctx context.Context, req ExtractRequest, archive io.Reader) error
}

// extractScript unpacks the archive on stdin into a private staging
// directory next to the destination and moves the entry into place only
// when the whole archive arrived. A failure or a cut stream removes the
// staging directory. It needs sh, tar, mkdir, mv and rm, which busybox has.
//
// Arguments: dir, name, staging directory name, "new" or "replace", and a
// heartbeat file to touch while the copy runs (empty for none).
const extractScript = `set -u
dir=$1 name=$2 st=$3 mode=$4 hb=$5
fail() { printf 'kc: %s\n' "$*" >&2; exit 1; }
if ! command -v tar >/dev/null 2>&1; then
  printf 'kc: tar not found\n' >&2
  exit 127
fi
cd "$dir" 2>/dev/null || fail "cannot enter $dir"
hbpid=
if [ -n "$hb" ]; then
  ( while kill -0 $$ 2>/dev/null; do touch "$hb" 2>/dev/null; sleep 10; done ) </dev/null >/dev/null 2>&1 &
  hbpid=$!
fi
cleanup() {
  if [ -n "$hbpid" ]; then kill "$hbpid" 2>/dev/null; fi
  rm -rf "./$st"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM PIPE
mkdir "./$st" || fail "cannot create a staging directory in $dir"
tar -xf - -C "./$st" || fail "extracting the archive failed"
[ -f "./$st/` + UploadCompleteMarker + `" ] || fail "the archive is incomplete"
if [ -e "./$name" ] || [ -h "./$name" ]; then
  [ "$mode" = replace ] || fail "$name exists"
  [ -h "./$name" ] && fail "$name is a symbolic link"
  if [ -d "./$st/$name" ]; then
    [ -d "./$name" ] || fail "$name exists and is not a directory"
    ( cd "./$st" && tar -cf - "./$name" ) | tar -xf - || fail "merging into $name failed"
  else
    [ -f "./$name" ] || fail "$name exists and is not a regular file"
    mv -f "./$st/$name" "./$name" || fail "replacing $name failed"
  fi
else
  mv "./$st/$name" "./$name" || fail "moving $name into place failed"
fi
exit 0
`

// ExtractTar copies archive into req.Dir of the session's container: the
// equivalent of kubectl cp, which runs tar -xf - in the container. It runs
// in its own exec stream and returns when the container side finished.
func (s *nativeSession) ExtractTar(ctx context.Context, req ExtractRequest, archive io.Reader) error {
	return s.extractTar(ctx, req.Dir, req, archive)
}

// extractTar runs the extract script with dir, the directory as the
// session's container spells it.
func (s *nativeSession) extractTar(ctx context.Context, dir string, req ExtractRequest, archive io.Reader) error {
	if !s.execAvailable() {
		return fmt.Errorf("podfs: exec unavailable")
	}
	if !path.IsAbs(req.Dir) {
		return fmt.Errorf("podfs: copy target %q is not absolute", req.Dir)
	}
	if !validName(req.Name) {
		return fmt.Errorf("podfs: unsafe name %q", req.Name)
	}
	mode := "new"
	if req.Replace {
		mode = "replace"
	}
	staging := "." + req.Name + ".kc-partial-" + stagingSuffix()
	command := []string{"sh", "-c", extractScript, "kc-copy", dir, req.Name, staging, mode, s.spec.Heartbeat}
	ctrllog.FromContext(ctx).WithName("podfs").Info("exec tar extract",
		"namespace", s.spec.Namespace, "pod", s.spec.Pod, "container", s.spec.Container, "dir", dir, "name", req.Name, "replace", req.Replace)
	out := &tailBuffer{limit: tarStderrLimit}
	err := s.runExec(ctx, s.spec.Container, command, remotecommand.StreamOptions{Stdin: archive, Stdout: out, Stderr: out})
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return scriptError(err, out.String())
}

// scriptError turns the extract script's output into an error: its own
// "kc: " lines when there are any, else the tail of what tar printed.
func scriptError(err error, output string) error {
	var own, other []string
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case line == "kc: tar not found":
			return ErrTarMissing
		case strings.HasPrefix(line, "kc: "):
			own = append(own, strings.TrimPrefix(line, "kc: "))
		default:
			other = append(other, line)
		}
	}
	switch {
	case len(own) > 0 && len(other) > 0:
		return fmt.Errorf("%s: %s", strings.Join(own, "; "), other[len(other)-1])
	case len(own) > 0:
		return errors.New(strings.Join(own, "; "))
	case len(other) > 0:
		return fmt.Errorf("tar: %w: %s", err, other[len(other)-1])
	}
	return fmt.Errorf("tar: %w", err)
}

// ExtractTar copies into the target container through the helper, which
// sees the target's files under HelperRoot. The script touches the
// heartbeat file while the copy runs, so the helper stays up for a copy
// that takes longer than its idle timeout.
func (h *helperAwareSession) ExtractTar(ctx context.Context, req ExtractRequest, archive io.Reader) error {
	inner, ok := h.ExecSession.(*nativeSession)
	if !ok {
		return fmt.Errorf("podfs: the debug helper session cannot copy")
	}
	if !path.IsAbs(req.Dir) {
		return fmt.Errorf("podfs: copy target %q is not absolute", req.Dir)
	}
	return inner.extractTar(ctx, HelperPath(req.Dir), req, archive)
}

// validName accepts a single path element.
func validName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\x00")
}

func stagingSuffix() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

var (
	_ TarExtractor = (*nativeSession)(nil)
	_ TarExtractor = (*helperAwareSession)(nil)
)
