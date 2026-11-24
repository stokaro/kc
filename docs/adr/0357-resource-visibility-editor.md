# 0357. Resource visibility editor, named profiles and bindings

- Status: Accepted
- Date: 2026-11-24
- Issue: #319
- Supersedes: decision 5 of [0354](0354-work-profile-in-type-lists.md)
  and its consequence that quick search finds listed rows only

## Context

ADR 0319 decides what each visibility mode lists, and ADR 0354 how the
type lists apply a profile. Under 0354 a panel lists Work or All, from
`resources.visibility.defaultProfile` or the "Types" row of the panel
options. Issue #319 also asks for Show, Hide and "use default" in Work,
Hide-selected and Only-selected profiles with multiselect, named
profiles, a global default, exact kube-context bindings with the
precedence panel override > context binding > global default > Work,
an editor usable at 80x24 from the Left and Right menu and the panel
options, explicit members listed at zero or while counting, selectors
for undiscovered types kept, and a type search that finds what the
current view leaves out.

## Decision

1. **Configuration.** `resources.visibility` holds `defaultProfile`,
   `profiles` and `contexts`. A profile is a `mode` (`work`, `hide`,
   `only`, `all`) and `show` and `hide` lists of exact `{group,
   resource}` selectors. `all` is built in and cannot be defined;
   `work` can be, as Work with the user's additions and removals, and
   must stay mode `work`. Profile names are DNS labels. `contexts` maps
   an exact context name to a profile name; an empty value binds
   nothing. `Validate` reports bad names, modes and selectors, a type
   both shown and hidden, lists a mode does not take, and a default or
   binding that names no profile. `Default()` is unchanged.

2. **Resolution.** A panel lists under its own choice, else the
   binding of the current context, else `defaultProfile`, else Work. A
   choice that is missing or invalid resolves to Work with the error
   attached, never to All. kc reports invalid settings once at start
   (a toast and a log line); the folders only log.

3. **The panel's own choice lives on the panel's configuration.**
   Each panel keeps its own copy of the settings (as for every other
   option), and its folders read the profile from it. `Enter` in the
   editor, or a Types choice in the panel options, sets a panel
   override with the profile, its name and whether it differs from the
   saved profile of that name (shown as `*`). An override equal to
   what the saved settings give the context drops itself, so the panel
   follows the settings again. A namespace change keeps the override,
   a panel swap carries it, a context change drops both panels'
   overrides.

4. **Saving has explicit destinations.** `F2` in the editor asks for a
   name and has two check boxes: use the profile for the current
   context, and use it as the global default. Saving writes the
   profile and the boxes' choices to `a.cfg`, the file and the target
   panel's copy; the other panel keeps its copy until the context
   changes, so editing a shared profile does not change a panel that
   did not ask for it. Clearing a box for the profile that holds it
   unbinds the context (an empty value, because the store merges maps
   key by key) or makes Work the default. The dialog suggests the
   current name only for a saved profile of the same mode. `C-s` in
   the panel options saves a Types choice as the context's binding
   when the context has one, else as the default, and writes nothing
   when the settings already give that profile.

5. **One editor for every mode.** The editor lists the context's
   types in both scopes from cached discovery: type, version, scope,
   whether the draft lists it (`if any` while "Include empty" is No
   and the type is not selected by name) and why, with a heading that
   says what a mark means. Each mode keeps its own marks, so trying
   Only selected does not lose Work's changes. `Space`, `Insert` and a
   click change one mark; `F5`, `F6` and `F8` change the discovered
   types the filter shows, whose count the summary line gives. A
   selected type that discovery does not report stays as a row,
   "not discovered" or "discovery failed" for a group that did not
   answer, and is saved again.

6. **Selected types are listed in any state.** A Work Show selector or
   an Only member gets a row while empty, counting, denied or failed,
   and its count cell says `0`, `…`, `denied` or `n/a`. The non-empty
   filter keeps applying to the other types.

7. **Quick search finds hidden types.** In a type list, text that
   matches no row finds a type of that list without a row: left out by
   the profile, empty or not accessible. The search label takes the
   mark color with the reason, the details line says what `Enter`
   opens, and `Enter` opens it as an explicit jump. Rows always win.

8. **Settings before folders.** kc reads the configuration file before
   it builds the start panels, and `Init` keeps the panel
   configurations the folders hold. The folders keep the configuration
   they were made with; read later, the file reached them only after a
   namespace or context change.

## Alternatives considered

- **A dialog per mode, or a profile manager plus a type picker.** More
  screens to learn and to fit at 80x24. One list with a mode switch
  shows the effect of each mode on the same rows.
- **Live preview while editing.** Applying each mark to the panel
  would start and stop counts per keystroke. The editor computes the
  effect from discovery alone and the panel changes on `Enter`.
- **Save to the shared settings and refresh every panel.** Simpler, but
  #319 asks that a panel keep its view unless it is the target.
- **Delete and rename profiles in kc.** Left for later; the file holds
  them, and a rename would have to rewrite bindings and the default.
- **Context patterns.** #319 asks for exact bindings; a pattern could
  bind a production context by accident.

## Consequences

- A configuration from 0354 (`defaultProfile: work` or `all`) means
  the same. Profiles written by kc carry `show` and `hide` lists, empty
  where unused.
- Adding a type to Work is a filter and a `Space` in the editor; the
  Other types dialog and quick search only open types.
- Settings that folders read (custom columns, node shell, port-forward
  address and others) now apply to the start panels as well.
