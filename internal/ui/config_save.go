package ui

import (
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/pkg/appconfig"
)

// configSavedMsg reports the outcome of a "Save as defaults".
type configSavedMsg struct {
	path string
	err  error
}

const (
	configToastTTL      = 8 * time.Second
	configLoadToastTTL  = 15 * time.Second
	sessionOnlyNotice   = "Applied to this session only; C-s saves as defaults"
	sessionOnlyNotSaved = "Not saved, applied to this session only: "
)

// configStore returns the store behind ~/.kc/config.yaml, creating it on
// first use. A store whose path is unknown refuses every save.
func (a *App) configStore() *appconfig.Store {
	if a.cfgStore == nil {
		p, _ := appconfig.DefaultPath()
		a.cfgStore = appconfig.NewStore(p)
	}
	return a.cfgStore
}

// loadConfig reads the configuration file into a.cfg. A file that does not
// load leaves the defaults in place and returns a toast that says so; the
// store then refuses to save over that file.
func (a *App) loadConfig() tea.Cmd {
	cfg, err := a.configStore().Load()
	*a.cfg = *cfg
	if err == nil {
		return nil
	}
	log := ctrllog.FromContext(a.ctx).WithName("config")
	log.Error(err, "configuration did not load; using defaults and refusing to save over it")
	return a.ShowToast(fmt.Sprintf("Config ignored, using defaults (saving is off): %v", err), configLoadToastTTL)
}

// saveDefaults writes change into the configuration file in the background.
// The store reads the file again first, so only the fields change sets are
// written and a file that does not load is never replaced.
func (a *App) saveDefaults(change func(*appconfig.Config)) tea.Cmd {
	store := a.configStore()
	return func() tea.Msg {
		_, err := store.Update(change)
		return configSavedMsg{path: store.Path(), err: err}
	}
}

// handleConfigSaved tells the user whether the settings reached the file.
func (a *App) handleConfigSaved(msg configSavedMsg) tea.Cmd {
	if msg.err == nil {
		return a.ShowInfo("Saved as defaults to "+msg.path, configToastTTL)
	}
	log := ctrllog.FromContext(a.ctx).WithName("config")
	log.Error(msg.err, "saving defaults failed", "path", msg.path)
	if _, ok := errors.AsType[*appconfig.LoadError](msg.err); ok {
		return a.ShowToast(sessionOnlyNotSaved+msg.err.Error()+"; fix the file and save again", configToastTTL)
	}
	return a.ShowToast(sessionOnlyNotSaved+msg.err.Error(), configToastTTL)
}

// sessionOnly tells the user that settings applied with Enter last only for
// this session.
func (a *App) sessionOnly() tea.Cmd {
	return a.ShowInfo(sessionOnlyNotice, 3*time.Second)
}
