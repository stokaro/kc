package ui

import (
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stokaro/kc/internal/models"
	table "github.com/stokaro/kc/internal/table"
	panelcontent "github.com/stokaro/kc/internal/ui/panelcontent"
)

// markTestApp lists "..", web-0, web-1, api-0 and db-0 in the left panel.
func markTestApp(t *testing.T) *App {
	t.Helper()
	rows := []table.Row{models.BackItem{}}
	for _, n := range []string{"web-0", "web-1", "api-0", "db-0"} {
		rows = append(rows, models.NewSimpleItem(n, []string{n}, nil, models.WhiteStyle()))
	}
	app := execTestApp(t, rows...)
	t.Cleanup(app.cancel)
	return app
}

// markedNames lists the marked rows of the left panel in panel order.
func markedNames(app *App) []string {
	var out []string
	for _, it := range app.leftPanel.GetSelectedItems() {
		id, _, _, _ := it.Item.Columns()
		out = append(out, id)
	}
	return out
}

// sendApp hands msg to the app and returns the messages its command
// produces.
func sendApp(app *App, msg tea.Msg) []tea.Msg {
	_, cmd := app.Update(msg)
	return collect(cmd)
}

// answerMarkGroup types pattern into the open pattern dialog, presses Enter
// and delivers the answer.
func answerMarkGroup(t *testing.T, app *App, pattern string) {
	t.Helper()
	if !(app.modalManager.TopName() == markGroupModal) {
		t.Fatal("the pattern dialog is not open")
	}
	keys := []tea.KeyPressMsg{{Code: tea.KeyBackspace}}
	for _, r := range pattern {
		keys = append(keys, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	keys = append(keys, tea.KeyPressMsg{Code: tea.KeyEnter})
	for _, msg := range pressDialog(t, app, keys...) {
		if res, ok := msg.(markGroupResultMsg); ok {
			sendApp(app, res)
		}
	}
	if app.modalManager.TopName() == markGroupModal {
		t.Fatal("the pattern dialog stayed open after Enter")
	}
}

// openMarkGroup runs a key or menu command and delivers the dialog request
// it makes.
func openMarkGroup(t *testing.T, app *App, msgs []tea.Msg, mark bool) {
	t.Helper()
	for _, msg := range msgs {
		if req, ok := msg.(panelcontent.MarkGroupRequest); ok {
			if req.Mark != mark {
				t.Fatalf("request Mark = %v, want %v", req.Mark, mark)
			}
			sendApp(app, req)
			return
		}
	}
	t.Fatalf("no pattern dialog request in %v", msgs)
}

func TestGroupMarkKeys(t *testing.T) {
	app := markTestApp(t)
	press := func(r rune) []tea.Msg { return sendApp(app, tea.KeyPressMsg{Code: r, Text: string(r)}) }

	press('*')
	if got, want := markedNames(app), []string{"web-0", "web-1", "api-0", "db-0"}; !slices.Equal(got, want) {
		t.Fatalf("after *: marked %v, want %v (never ..)", got, want)
	}
	openMarkGroup(t, app, press('-'), false)
	answerMarkGroup(t, app, "WEB-*")
	if got, want := markedNames(app), []string{"api-0", "db-0"}; !slices.Equal(got, want) {
		t.Fatalf("after - WEB-*: marked %v, want %v", got, want)
	}
	press('*')
	if got, want := markedNames(app), []string{"web-0", "web-1"}; !slices.Equal(got, want) {
		t.Fatalf("after the second *: marked %v, want %v", got, want)
	}
	openMarkGroup(t, app, press('+'), true)
	answerMarkGroup(t, app, "?b-[0-9]")
	if got, want := markedNames(app), []string{"web-0", "web-1", "db-0"}; !slices.Equal(got, want) {
		t.Fatalf("after + ?b-[0-9]: marked %v, want %v", got, want)
	}
	sendApp(app, tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl})
	if got := markedNames(app); len(got) != 4 {
		t.Fatalf("after C-a: marked %v, want all four", got)
	}
}

func TestGroupMarkMenu(t *testing.T) {
	app := markTestApp(t)
	run := func(label string) []tea.Msg {
		t.Helper()
		for _, item := range app.fileMenuItems() {
			if item.Label == label {
				if item.Disabled {
					t.Fatalf("%s is dim on a list", label)
				}
				return collect(item.Run())
			}
		}
		t.Fatalf("no %s in the File menu", label)
		return nil
	}
	openMarkGroup(t, app, run("&Select group"), true)
	answerMarkGroup(t, app, "web-*")
	if got, want := markedNames(app), []string{"web-0", "web-1"}; !slices.Equal(got, want) {
		t.Fatalf("Select group web-*: marked %v, want %v", got, want)
	}
	run("&Invert selection")
	if got, want := markedNames(app), []string{"api-0", "db-0"}; !slices.Equal(got, want) {
		t.Fatalf("Invert selection: marked %v, want %v", got, want)
	}
	openMarkGroup(t, app, run("&Unselect group"), false)
	answerMarkGroup(t, app, "*")
	if got := markedNames(app); len(got) != 0 {
		t.Fatalf("Unselect group *: marked %v, want none", got)
	}
}

func TestGroupMarkDialog(t *testing.T) {
	t.Run("Esc cancels", func(t *testing.T) {
		app := markTestApp(t)
		sendApp(app, panelcontent.MarkGroupRequest{Mark: true})
		for _, msg := range pressDialog(t, app, tea.KeyPressMsg{Code: tea.KeyEscape}) {
			sendApp(app, msg)
		}
		if app.modalManager.TopName() == markGroupModal {
			t.Fatal("Esc left the dialog open")
		}
		if got := markedNames(app); len(got) != 0 {
			t.Fatalf("Esc marked %v", got)
		}
	})
	t.Run("a bad pattern is refused in the dialog", func(t *testing.T) {
		app := markTestApp(t)
		sendApp(app, panelcontent.MarkGroupRequest{Mark: true})
		keys := []tea.KeyPressMsg{{Code: '[', Text: "["}, {Code: tea.KeyEnter}}
		for _, msg := range pressDialog(t, app, keys...) {
			if res, ok := msg.(markGroupResultMsg); ok && res.confirm {
				t.Fatalf("the dialog accepted %q", res.pattern)
			}
		}
	})
	t.Run("no match says so", func(t *testing.T) {
		app := markTestApp(t)
		msgs := collect(app.markGroup(0, "nothing*", true))
		if len(msgs) != 1 {
			t.Fatalf("messages %v, want one toast", msgs)
		}
		if toast, ok := msgs[0].(showToastMsg); !ok || toast.text != `No unmarked row matches "nothing*"` {
			t.Fatalf("message %#v", msgs[0])
		}
	})
	t.Run("not outside a list", func(t *testing.T) {
		app := markTestApp(t)
		app.leftPanel.mode = PanelModeDescribe
		if sendApp(app, panelcontent.MarkGroupRequest{Mark: true}); app.modalManager.TopName() == markGroupModal {
			t.Fatal("the dialog opened on a Describe panel")
		}
	})
}

// "+", "-" and "*" belong to the shell line while it holds text, as in mc.
func TestGroupMarkKeysGoToTypedShellLine(t *testing.T) {
	app := markTestApp(t)
	for _, key := range []string{"+", "-", "*"} {
		if !app.shouldRouteToPanel(key) {
			t.Fatalf("%q with an empty shell line went to the shell", key)
		}
	}
	typeOnShellLine(app, "kubectl get pods")
	for _, key := range []string{"+", "-", "*"} {
		if app.shouldRouteToPanel(key) {
			t.Fatalf("%q went to the panel while the shell line has text", key)
		}
	}
}
