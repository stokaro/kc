# Custom Commands Design

## Overview
This document outlines the design and requirements for the "Custom Commands" feature in `kc`. This feature allows users to define and execute custom shell commands within the application, integrated into the panel system.

## Background
Custom commands turn a panel into a context-aware terminal powered by `bubbleterm`. They are defined in `config.yaml`, surfaced through the F2 user menu (mc's user menu; also Command > User menu in the F9 menu bar) (instead of hard-to-discover hotkeys), and can optionally take over the full screen. The menu is ordered by type so the most generally applicable commands appear first (global → namespace → sticky → selector).

## Requirements

### Core Functionality
1.  **Execution Context**: Commands run within a `bubbleterm` terminal emulator inside a panel.
2.  **Command Types**:
    *   **Dynamic (`selector`)**: Triggered by the current selection. Reruns when the selection changes. Shows output only.
    *   **Sticky (`sticky`)**: Receives the selection at startup but keeps running independently of subsequent selection changes.
    *   **Namespace-Sticky (`namespace`)**: Sticky for the current namespace. Restarts when the namespace changes.
    *   **Global (`global`)**: Completely independent of selection (e.g., `kubectl top nodes`).
3.  **Templating**: Commands use Go templates to inject context variables:
    *   `.Name`: Name of the selected item.
    *   `.Namespace`: Namespace of the selected item.
    *   `.Kind`: Kind of the selected item.
    *   `.Group`: API Group.
    *   `.Version`: API Version.
    *   `.Resource`: Resource name (plural).
    *   `.Items`: List of selected items (for multi-selection).
4.  **Configuration**: Commands are defined in `config.yaml`.
5.  **Menu Integration**: Accessible via the F2 user menu, grouped by type.
6.  **Filtering**: Commands are only shown if applicable to the current selection (Resource, Group).
7.  **Interactive Mode**: Commands can be interactive (receive input).
8.  **Multi-selection**: Optional support for multiple selected items.
9.  **Namespace visibility**: Namespace commands stay visible even when no object is selected, as long as a namespace is active.

### UI/UX
*   **F2 User Menu**: A modal menu to list and select applicable commands.
*   **Panel Integration**: Commands take over the panel view when running.
*   **Terminal**: Uses `bubbleterm` for robust terminal emulation.
*   **Debounce**: Configurable debounce time for dynamic commands to prevent excessive execution during rapid navigation.
*   **Ordering**: Menu sections ordered by type: global, namespace, sticky, selector.

### Configuration Structure
```yaml
commands:
  - name: "Describe Pod"
    command: "kubectl describe pod {{.Name}} -n {{.Namespace}}"
    type: "selector" # selector, sticky, namespace, global
    location: "panel" # panel, fullscreen
    interactive: false
    debounce: "500ms"
    onExit: "keep-open" # keep-open, close, restore
    showFor:
      resources: ["pods"]
      groups: [""]
    supportsMultiSelection: false
```

## Applicability Rules (F2 user menu)
Built-in object actions (`internal/objectaction`, see `docs/adr/0036-object-action-registry.md`) come first, under "Object Actions", when exactly one object is selected. The rules below apply to the configured commands.
- **global**: always visible; no selection required.
- **namespace**: visible when the panel has an active namespace, even without a selected object; if an object is selected, still honor filters.
- **selector / sticky**: require a selected object; hidden otherwise.
- **Multi-selection**: hidden when multiple items are selected unless `supportsMultiSelection` is true.
- **Filters**: when an object is present, apply `showFor.resources` (plural) and `showFor.groups` (empty string matches core) case-insensitively; if filters are empty, any object passes.
- **Invalid commands**: hidden rather than disabled to keep the menu concise.

## Implementation Details

### Components
1.  **Config**: `CommandConfig` struct in `pkg/appconfig`.
2.  **Widget**: `CommandWidget` in `internal/ui/panel_command.go`.
    *   Embeds `bubbleterm.Model`.
    *   Handles command execution via `os/exec`.
    *   Manages debounce logic.
    *   Handles template rendering.
3.  **Panel Integration**:
    *   New `PanelModeCommand` in `internal/ui/panel_modes.go`.
    *   `StartCommand` method in `Panel` to switch mode and start widget.
4.  **Menu**:
    *   `PanelActionMenu` (F2) handler in `internal/ui/app.go`.
    *   `CommandSelectorModel` (to be implemented) for the menu UI.

### Templating
*   Uses `text/template`.
*   Context derived from `models.Item` (specifically `models.ObjectItem`).

### Hotkeys, prompts and confirmation (#46, ADR 0046)
*   `hotkey`, `prompts` and `confirm` live on `CommandConfig`; `appconfig.ValidateCommands` checks them once at startup (`App.loadCommandHotkeys`) and builds the hotkey table without changing the loaded configuration.
*   A hotkey opens the commands menu of the active panel and picks its entry (`CommandSelectorModel.selectCommand`), so the applicability rules and the target are the same as for Enter in the menu. `C-x` chords use a small prefix state in `commandHotkeys`.
*   The menu callback is wrapped by `App.guardCommand` (`command_guard.go`): prompt dialog (`CommandPromptModel`), then the red `ConfirmModel`, then the start. `cmdtemplate.Bind` substitutes the answers into the template text, so the bound command is what the panel stores and restarts.

### Future Considerations
*   **Background Color**: Configurable panel background (blue/black).
*   **Stop on Mode Change**: Option to stop commands when switching panel modes.
