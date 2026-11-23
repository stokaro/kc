package dashboard

import (
	"strconv"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/stokaro/kc/internal/models"
)

// selectedPods are the pods whose containers a short list shows under it:
// the selected pod, or the pods of the selected workload.
func selectedPods(v *view, it targetItem) []models.UsageRow {
	switch {
	case it.up:
		return nil
	case it.row.Workload == nil:
		return []models.UsageRow{it.row}
	}
	var out []models.UsageRow
	for _, p := range v.now.PodRows {
		if p.Owner != nil && *p.Owner == *it.row.Workload {
			out = append(out, p)
		}
	}
	return out
}

// containerLines are the containers of pods, under a blank line and a
// heading per pod: whether each is ready, its restarts, what it does now
// and, on a second line, why it last stopped.
func (m *Model) containerLines(pods []models.UsageRow, w int) []string {
	var lines []string
	for _, p := range pods {
		if len(p.Containers) == 0 {
			continue
		}
		lines = append(lines, "", headStyle().Render(fit("Containers of pod "+p.Name, w)))
		for _, c := range p.Containers {
			lines = append(lines, m.containerLine(c, w)...)
			if c.LastReason != "" {
				st := noteStyle()
				if c.LastReason == "OOMKilled" || c.LastExit != 0 {
					st = badStyle()
				}
				text := "    last stopped " + m.clock(c.LastAt) + ": " + c.LastReason + ", exit " + strconv.Itoa(int(c.LastExit))
				lines = append(lines, st.Render(fit(text, w)))
			}
		}
	}
	return lines
}

// containerLine is one container: name, readiness, restarts and state,
// the state on a line of its own when the list is too narrow for both.
func (m *Model) containerLine(c models.UsageContainer, w int) []string {
	name := c.Name
	if c.Init {
		name += " (init)"
	}
	ready, readySt := "ready", baseStyle()
	if !c.Ready {
		ready = "not ready"
		if !c.Init || c.State != "terminated" {
			readySt = badStyle()
		}
	}
	state, stateSt := c.State, noteStyle()
	switch c.State {
	case "running":
		if !c.Since.IsZero() {
			state += " since " + m.clock(c.Since)
		}
	case "waiting":
		state, stateSt = "waiting: "+c.Reason, badStyle()
	case "terminated":
		state = "terminated: " + c.Reason
		if c.Reason != "Completed" {
			stateSt = badStyle()
		}
	case "":
		state = "no state reported"
	}
	rst := plural(int(c.Restarts), "restart")
	line := baseStyle().Render("  "+name+"  ") + readySt.Render(ready) + baseStyle().Render("  "+rst)
	if ansi.StringWidth(line)+2+ansi.StringWidth(state) <= w {
		return []string{fit(line+baseStyle().Render("  ")+stateSt.Render(state), w)}
	}
	return []string{fit(line, w), stateSt.Render(fit("    "+state, w))}
}

// clock writes t as a time of day, with the date when it is not today.
func (m *Model) clock(t time.Time) string {
	t = t.In(m.loc())
	if now := m.now().In(m.loc()); t.YearDay() != now.YearDay() || t.Year() != now.Year() {
		return t.Format("01-02 15:04")
	}
	return t.Format("15:04:05")
}
