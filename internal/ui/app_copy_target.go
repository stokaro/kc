package ui

import "os"

// copyDialogDir is the directory the copy-to-local dialog proposes: the
// directory the other panel shows when it lists the local filesystem (as
// mc copies to the other panel), and the system temp directory otherwise,
// so an export never lands in the working directory by accident.
func (a *App) copyDialogDir() string {
	if panel := a.panelByIndex(1 - a.activePanel); panel != nil {
		if nav := a.navigatorForPanel(panel); nav != nil {
			if local, ok := nav.Current().(localFolder); ok && local.LocalDir() != "" {
				return local.LocalDir()
			}
		}
	}
	return os.TempDir()
}
