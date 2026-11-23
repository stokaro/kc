# 0237. Context help: one page per feature, opened by F1

- Status: Accepted
- Date: 2026-11-23
- Pull request: #237

## Context

F1 showed the whole README in a window as wide as the terminal. The user
opened the investigation window (`C-x w`), pressed F1 and learned nothing
about it. mc's F1 opens the help node of the dialog or screen in focus,
in a window of fixed width, with links between nodes and its own key bar.

The README had grown to describe each feature in long sections and in
paragraphs after the key table. Copying that text into help pages would
give two descriptions to keep in step.

## Decision

### Layout

- `internal/help` embeds `topics/*.md` with `go:embed`. A page's ID is its
  file name without `.md`; its title is the first `# ` line. `ids.go`
  declares a constant for every page the code opens.
- Pages link with relative links, `[Row filter](row-filter.md)`. The same
  links work when the files are read on GitHub. `index.md` groups and
  links every page; `help.md` explains the help window.
- Pages are short and written for an operator: what it is, how to read
  it, keys, limits, see also. No tables and no code lines over 70
  columns, since the window wraps at 76.

### Source of truth

The pages are the reference for how a feature behaves. The README keeps
installation, highlights, the key table and the configuration reference
(with its YAML examples); its feature sections now give one or two
sentences and link to the page. Changing a feature means changing its
page; the README changes only when the summary or a key does.

Deriving pages from README sections was rejected: the README is
organized for reading on GitHub, not per dialog, and a section rarely
matches one dialog.

### Context mapping

`internal/ui/help_context.go` holds the whole mapping:

1. A dialog on the modal stack: `modalHelpPages`, keyed by the name the
   dialog is registered under in `ModalManager.Register`. F1 is caught
   in `Modal.Update`, after the `Esc` digit fallback, so `Esc 1` works
   too. The help window has no page and handles F1 itself.
2. A panel not in List mode: `modeHelpPages`.
3. A List panel: the row under the cursor when it enters a special
   folder (by the last path segment: `/diagnosis`, `/timeline`,
   `/attention`, quiet checks, `/owners`, ...) or is a logs row; object
   rows never count, so an object named "timeline" stays an object.
   Otherwise the folder, by its type name in `folderHelpPages`, refined
   for related folders (diagnosis by kind) and overview findings (quiet
   checks by path).
4. Anything else opens the index.

The help window is pushed onto the modal stack over the dialog. The
dialog is not touched, so it keeps its state, and closing the help shows
it again. Every dialog with a page gets "F1 Help" in its key bar from
`Modal.buildFooter`, in slot 1 of a numbered bar or first in a short one.

### The window

Centered, at most 80 columns (76 of text, padding and frame), most of
the screen high, the screen less a margin when it is narrower. Its key
bar is mc's: 1 Help, 2 Index, 3 Prev, 10 Quit. Links come to glamour as
their text between two private-use runes; after rendering, kc records
where each link sits and paints it black on cyan, the selected one white
on blue, as mc's help skin does. `Tab` selects, `Enter` or a click
follows, `F3` goes back to the previous page and its scroll position.

### Tests

The UI tests parse the package source for `modalManager.Register` calls
and the `internal/models` source for folder struct types; a dialog or
folder type without an entry fails, and so does an entry for a name that
no longer exists. Every panel mode must have a page. The help package
tests check that IDs and files match, every link target exists, the
index links every page, and every page renders.

## Consequences

- A new dialog or folder type needs a page (or an existing one) before
  the tests pass.
- README.md is no longer embedded in the binary.
- Writing the pages from the code found differences with the README and
  the ADRs: the registered "Panel Mode" dialog is never shown; dangerous
  commands are hidden from the F2 user menu, not the F9 menu bar; Select
  group and Unselect group in the File menu do nothing; `M-d` and `M-z`
  are not in the list of built-in hotkeys a custom command may not take;
  stopping a port forward with F8 is refused in read-only mode; the
  logs row opens with F3, not Enter. The pages follow the code. These
  are left for follow-ups.
