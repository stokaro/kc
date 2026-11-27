package ui

import (
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stokaro/kc/pkg/appconfig"
)

// configSavedMsg reports the outcome of a save to the configuration file;
// what names the change for the toast ("as defaults", "profile shop").
type configSavedMsg struct {
	what string
	path string
	err  error
}

const (
	configToastTTL      = 8 * time.Second
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
// load in full keeps the sections that did and the defaults for the others,
// and every context is read-only when its readOnly settings did not load
// (appconfig.Store.Load). The error stays on screen for the session, above
// the command line, and the store refuses to save over that file.
func (a *App) loadConfig() {
	cfg, err := a.configStore().Load()
	*a.cfg = *cfg
	a.cfgLoadErr = nil
	if err == nil {
		return
	}
	le, ok := errors.AsType[*appconfig.LoadError](err)
	if !ok {
		le = &appconfig.LoadError{Path: a.configStore().Path(), Err: err}
	}
	a.cfgLoadErr = le
	log := ctrllog.FromContext(a.ctx).WithName("config")
	log.Error(err, "configuration did not load in full; refusing to save over it",
		"path", le.Path, "defaultSections", le.Sections, "everyContextReadOnly", le.ReadOnlyLost())
}

// saveDefaults writes change into the configuration file in the background.
// The store reads the file again first, so only the fields change sets are
// written and a file that does not load is never replaced.
func (a *App) saveDefaults(change func(*appconfig.Config)) tea.Cmd {
	return a.saveSettings("as defaults", change)
}

// saveSettings is saveDefaults for a change that is not a default, such
// as a saved profile; the toast says "Saved <what> to <path>".
func (a *App) saveSettings(what string, change func(*appconfig.Config)) tea.Cmd {
	store := a.configStore()
	return func() tea.Msg {
		_, err := store.Update(change)
		return configSavedMsg{what: what, path: store.Path(), err: err}
	}
}

// handleConfigSaved tells the user whether the settings reached the file.
func (a *App) handleConfigSaved(msg configSavedMsg) tea.Cmd {
	if msg.err == nil {
		return a.ShowInfo("Saved "+msg.what+" to "+msg.path, configToastTTL)
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
