package podfs

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/tools/remotecommand"
)

// localExec is a fake exec stream: it runs the command on this machine,
// with env, wired to the streams. It runs the real extract script with the
// local sh and tar, which proves the script's logic, not a container's tar.
func localExec(env []string, record *[]string) execFunc {
	return func(ctx context.Context, container string, command []string, streams remotecommand.StreamOptions) error {
		if record != nil {
			*record = append([]string{container}, command...)
		}
		// #nosec G204 -- the fake runs the script the test hands to tar extraction.
		cmd := exec.CommandContext(ctx, command[0], command[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr, cmd.Env = streams.Stdin, streams.Stdout, streams.Stderr, env
		return cmd.Run()
	}
}

func needLocalTar(t *testing.T) []string {
	t.Helper()
	for _, bin := range []string{"sh", "tar", "mkdir", "mv", "rm"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found: %v", bin, err)
		}
	}
	// COPYFILE_DISABLE keeps macOS tar from adding AppleDouble members.
	return []string{"PATH=" + os.Getenv("PATH"), "COPYFILE_DISABLE=1"}
}

type tarMember struct {
	name, data string
	dir        bool
}

func buildArchive(t *testing.T, members []tarMember, marker bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, m := range members {
		hdr := &tar.Header{Name: m.name, Mode: 0o644, Size: int64(len(m.data)), Typeflag: tar.TypeReg, Format: tar.FormatGNU, ModTime: time.Unix(1700000000, 0)}
		if m.dir {
			hdr.Typeflag, hdr.Mode, hdr.Size = tar.TypeDir, 0o755, 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, m.data); err != nil {
			t.Fatal(err)
		}
	}
	if marker {
		if err := tw.WriteHeader(&tar.Header{Name: UploadCompleteMarker, Mode: 0o600, Typeflag: tar.TypeReg, Format: tar.FormatGNU}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func readTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := make(map[string]string)
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		switch {
		case d.Type()&os.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			out[rel] = "-> " + target
		case d.IsDir():
			out[rel+"/"] = ""
		default:
			// #nosec G122 -- the test reads back a temporary tree it created.
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[rel] = string(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestExtractScript(t *testing.T) {
	env := needLocalTar(t)
	dirTree := []tarMember{{name: "app/", dir: true}, {name: "app/a.txt", data: "new a"}, {name: "app/sub/", dir: true}, {name: "app/sub/b.txt", data: "b"}}
	cases := []struct {
		name    string
		prepare func(t *testing.T, dir string)
		entry   string
		replace bool
		archive func(t *testing.T) []byte
		wantErr string
		want    map[string]string
	}{
		{
			name: "new directory", entry: "app",
			archive: func(t *testing.T) []byte { return buildArchive(t, dirTree, true) },
			want:    map[string]string{"app/": "", "app/a.txt": "new a", "app/sub/": "", "app/sub/b.txt": "b"},
		},
		{
			name: "new file", entry: "f.txt",
			archive: func(t *testing.T) []byte { return buildArchive(t, []tarMember{{name: "f.txt", data: "hi"}}, true) },
			want:    map[string]string{"f.txt": "hi"},
		},
		{
			name: "existing without replace", entry: "f.txt",
			prepare: func(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, "f.txt"), "old") },
			archive: func(t *testing.T) []byte { return buildArchive(t, []tarMember{{name: "f.txt", data: "hi"}}, true) },
			wantErr: "f.txt exists",
			want:    map[string]string{"f.txt": "old"},
		},
		{
			name: "replace a file", entry: "f.txt", replace: true,
			prepare: func(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, "f.txt"), "old") },
			archive: func(t *testing.T) []byte { return buildArchive(t, []tarMember{{name: "f.txt", data: "hi"}}, true) },
			want:    map[string]string{"f.txt": "hi"},
		},
		{
			name: "merge into a directory", entry: "app", replace: true,
			prepare: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, "app", "a.txt"), "old a")
				writeFile(t, filepath.Join(dir, "app", "keep.txt"), "keep")
			},
			archive: func(t *testing.T) []byte { return buildArchive(t, dirTree, true) },
			want:    map[string]string{"app/": "", "app/a.txt": "new a", "app/keep.txt": "keep", "app/sub/": "", "app/sub/b.txt": "b"},
		},
		{
			name: "symbolic link in the way", entry: "app", replace: true,
			prepare: func(t *testing.T, dir string) {
				if err := os.Symlink("/etc", filepath.Join(dir, "app")); err != nil {
					t.Fatal(err)
				}
			},
			archive: func(t *testing.T) []byte { return buildArchive(t, dirTree, true) },
			wantErr: "app is a symbolic link",
			want:    map[string]string{"app": "-> /etc"},
		},
		{
			name: "file in the way of a directory", entry: "app", replace: true,
			prepare: func(t *testing.T, dir string) { writeFile(t, filepath.Join(dir, "app"), "file") },
			archive: func(t *testing.T) []byte { return buildArchive(t, dirTree, true) },
			wantErr: "not a directory",
			want:    map[string]string{"app": "file"},
		},
		{
			name: "archive without the marker", entry: "app",
			archive: func(t *testing.T) []byte { return buildArchive(t, dirTree, false) },
			wantErr: "incomplete",
			want:    make(map[string]string),
		},
		{
			name: "archive cut between members", entry: "app",
			archive: func(t *testing.T) []byte {
				full := buildArchive(t, dirTree, true)
				// Two members of 512-byte headers (a directory and a.txt
				// with one data block) and nothing after them.
				return full[:3*512]
			},
			wantErr: "incomplete|extracting the archive failed",
			want:    make(map[string]string),
		},
		{
			name: "archive cut inside a member", entry: "app",
			archive: func(t *testing.T) []byte { return buildArchive(t, dirTree, true)[:512+100] },
			wantErr: "incomplete|extracting the archive failed",
			want:    make(map[string]string),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.prepare != nil {
				tc.prepare(t, dir)
			}
			s := &nativeSession{spec: SessionSpec{Namespace: "ns", Pod: "p", Container: "c"}, exec: localExec(env, nil)}
			err := s.ExtractTar(t.Context(), ExtractRequest{Dir: dir, Name: tc.entry, Replace: tc.replace}, bytes.NewReader(tc.archive(t)))
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("ExtractTar: %v", err)
			case tc.wantErr != "" && !errorMatches(err, tc.wantErr):
				t.Fatalf("ExtractTar = %v, want %q", err, tc.wantErr)
			}
			got := readTree(t, dir)
			if len(got) != len(tc.want) {
				t.Fatalf("tree = %v, want %v (staging left behind?)", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("tree[%s] = %q, want %q (tree %v)", k, got[k], v, got)
				}
			}
		})
	}
}

func TestExtractScriptWithoutTar(t *testing.T) {
	needLocalTar(t)
	// A PATH without tar; sh is started by its absolute path.
	sh, _ := exec.LookPath("sh")
	env := []string{"PATH=" + t.TempDir()}
	run := localExec(env, nil)
	s := &nativeSession{spec: SessionSpec{Namespace: "ns", Pod: "p", Container: "c"}, exec: func(ctx context.Context, c string, command []string, st remotecommand.StreamOptions) error {
		command = append([]string{sh}, command[1:]...)
		return run(ctx, c, command, st)
	}}
	err := s.ExtractTar(t.Context(), ExtractRequest{Dir: t.TempDir(), Name: "f"}, bytes.NewReader(buildArchive(t, []tarMember{{name: "f", data: "x"}}, true)))
	if !errors.Is(err, ErrTarMissing) {
		t.Fatalf("ExtractTar = %v, want ErrTarMissing", err)
	}
}

// TestExtractScriptTouchesHeartbeat proves the helper stays up during a
// long copy: the stream does not end before the heartbeat file appears.
func TestExtractScriptTouchesHeartbeat(t *testing.T) {
	env := needLocalTar(t)
	dir := t.TempDir()
	hb := filepath.Join(t.TempDir(), "alive")
	archive := buildArchive(t, []tarMember{{name: "f", data: "x"}}, true)
	waiting := &waitForFile{path: hb, deadline: time.Now().Add(10 * time.Second), r: bytes.NewReader(archive)}
	s := &nativeSession{spec: SessionSpec{Namespace: "ns", Pod: "p", Container: "c", Heartbeat: hb}, exec: localExec(env, nil)}
	if err := s.ExtractTar(t.Context(), ExtractRequest{Dir: dir, Name: "f"}, waiting); err != nil {
		t.Fatalf("ExtractTar: %v", err)
	}
	if !waiting.seen {
		t.Fatalf("heartbeat %s was not touched while the archive was streaming", hb)
	}
}

// waitForFile holds the stream until path exists or the deadline passed.
type waitForFile struct {
	path     string
	deadline time.Time
	r        io.Reader
	seen     bool
}

func (w *waitForFile) Read(p []byte) (int, error) {
	for !w.seen && time.Now().Before(w.deadline) {
		if _, err := os.Stat(w.path); err == nil {
			w.seen = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return w.r.Read(p)
}

func TestHelperExtractUsesHelperRoot(t *testing.T) {
	var got []string
	inner := &nativeSession{spec: SessionSpec{Namespace: "ns", Pod: "p", Container: "kc-fs-helper-c-0", Heartbeat: helperHeartbeat}, exec: localExec(nil, &got)}
	h := &helperAwareSession{ExecSession: inner, container: "kc-fs-helper-c-0"}
	// The local sh fails to enter /proc/1/root/app; the command is what counts.
	_ = h.ExtractTar(t.Context(), ExtractRequest{Dir: "/app", Name: "f"}, strings.NewReader(""))
	if len(got) < 10 {
		t.Fatalf("command = %q", got)
	}
	if got[0] != "kc-fs-helper-c-0" || got[1] != "sh" || got[5] != "/proc/1/root/app" || got[6] != "f" || got[8] != "new" || got[9] != helperHeartbeat {
		t.Fatalf("command = %q", got)
	}
}

func TestExtractTarRefusesBadRequests(t *testing.T) {
	s := &nativeSession{exec: func(context.Context, string, []string, remotecommand.StreamOptions) error {
		t.Fatal("exec ran")
		return nil
	}}
	for _, req := range []ExtractRequest{{Dir: "rel", Name: "f"}, {Dir: "/d", Name: "../f"}, {Dir: "/d", Name: "a/b"}, {Dir: "/d", Name: ".."}, {Dir: "/d", Name: ""}} {
		if err := s.ExtractTar(t.Context(), req, strings.NewReader("")); err == nil {
			t.Errorf("ExtractTar(%+v) succeeded", req)
		}
	}
	if err := (&nativeSession{}).ExtractTar(t.Context(), ExtractRequest{Dir: "/d", Name: "f"}, strings.NewReader("")); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("ExtractTar without exec = %v", err)
	}
}

func TestScriptError(t *testing.T) {
	base := errors.New("command terminated with exit code 1")
	cases := []struct{ out, want string }{
		{"kc: f.txt exists\n", "f.txt exists"},
		{"tar: short read\nkc: extracting the archive failed\n", "extracting the archive failed: tar: short read"},
		{"weird\n", "tar: command terminated with exit code 1: weird"},
		{"", "tar: command terminated with exit code 1"},
	}
	for _, tc := range cases {
		if got := scriptError(base, tc.out).Error(); got != tc.want {
			t.Errorf("scriptError(%q) = %q, want %q", tc.out, got, tc.want)
		}
	}
	if !errors.Is(scriptError(base, "kc: tar not found\n"), ErrTarMissing) {
		t.Error("tar not found is not ErrTarMissing")
	}
}

func writeFile(t *testing.T, p, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// errorMatches reports whether err contains one of the |-separated parts:
// a cut archive fails in tar or at the marker check, depending on the tar.
func errorMatches(err error, want string) bool {
	if err == nil {
		return false
	}
	for part := range strings.SplitSeq(want, "|") {
		if strings.Contains(err.Error(), part) {
			return true
		}
	}
	return false
}

// TestHelperTarStream proves a download through the helper streams tar
// from the target's files and keeps the heartbeat fresh while it runs.
func TestHelperTarStream(t *testing.T) {
	env := needLocalTar(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "data", "a.txt"), "hello")
	hb := filepath.Join(t.TempDir(), "alive")
	var got []string
	run := localExec(env, &got)
	inner := &nativeSession{spec: SessionSpec{Namespace: "ns", Pod: "p", Container: "helper", Heartbeat: hb}, exec: func(ctx context.Context, c string, command []string, st remotecommand.StreamOptions) error {
		// This machine has no target under /proc/1/root; serve a fake root.
		command = append([]string(nil), command...)
		command[5] = strings.Replace(command[5], HelperRoot, root, 1)
		return run(ctx, c, command, st)
	}}
	h := &helperAwareSession{ExecSession: inner, container: "helper"}
	rc, err := h.TarStream(t.Context(), "/data", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := rc.Close(); err != nil {
			t.Errorf("closing tar stream: %v", err)
		}
	})
	tr := tar.NewReader(rc)
	hdr, err := tr.Next()
	if err != nil {
		t.Fatalf("reading the stream: %v (command %q)", err, got)
	}
	data, _ := io.ReadAll(tr)
	if hdr.Name != "./a.txt" || string(data) != "hello" {
		t.Fatalf("member %q = %q", hdr.Name, data)
	}
	if _, err := io.Copy(io.Discard, rc); err != nil {
		t.Fatalf("tar exit: %v", err)
	}
	if got[0] != "helper" || got[1] != "sh" || got[5] != hb || got[7] != "a.txt" {
		t.Fatalf("command = %q", got)
	}
	if _, err := os.Stat(hb); err != nil {
		t.Fatalf("heartbeat not touched: %v", err)
	}
}
