package ui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/internal/models"
)

// loadHealthConfig installs the health config of the loaded configuration
// for row colors, the faults filter and /diagnosis. Problems go to the log
// and to the command line; the entries that have them are not used.
func (a *App) loadHealthConfig() tea.Cmd {
	if a.cfg == nil {
		models.InstallHealthAdapters(nil)
		return nil
	}
	errs := models.InstallHealthAdapters(a.cfg.Health)
	if len(errs) == 0 {
		return nil
	}
	err := errors.Join(errs...)
	ctrllog.FromContext(a.ctx).WithName("health").Error(err, "health config has problems; those entries are not used")
	first := errs[0].Error()
	if n := len(errs) - 1; n > 0 {
		first = fmt.Sprintf("%s (and %d more, see the log)", first, n)
	}
	first = strings.ReplaceAll(first, "\n", "; ")
	if a.toastLogger != nil {
		return a.toastLogger.Errorf("Config: %s", first)
	}
	return a.ShowToast("Config: "+first, 15*time.Second)
}
