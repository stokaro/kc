# Resource visibility

Which resource types a type list shows is set by a visibility profile.
Work is the built-in one. The editor changes what a panel lists: add
types to Work or remove them, list only the types you check, hide the
types you check, or list every type. A profile can be saved under a
name, used for one kube context or made the global default.

## Where a panel's profile comes from

The first of these that is set wins:

1. the panel's own choice, made with `Enter` in the editor or with
   Types in the panel options; it lasts until the context changes or
   kc exits;
2. the profile of the current kube context,
   `resources.visibility.contexts`, matched by the exact context name;
3. the global default, `resources.visibility.defaultProfile`;
4. Work.

A profile that is not defined or not valid lists Work, never every
type. kc says so when it starts, and the editor shows the error.

## How to read it

"Resource visibility..." in the Left or Right menu (`F9`) and the row
of the same name in the panel options (`M-F1`, `M-F2`) open the editor
for that panel. It lists every type the context serves that kc can
browse, cluster-scoped and namespaced, from the cached discovery.

- The top line names the profile (`*` marks unsaved changes), the mode
  and the context. The line below says what a mark means in the mode.
- Columns: the mark, the type (plural name and API group), the version,
  the scope (`ns` or `cluster`), Listed and Why, the same reason the
  type list gives. Listed is `yes`, `no`, or `if any` while the panel's
  "Include empty" is No: the type has a row while it has objects. A
  narrow dialog drops Version, then Scope, then Listed. Types the
  profile leaves out are gray.
- A selected type that discovery does not report stays in the list
  with "not discovered", or "discovery failed" when its API group did
  not answer. It is kept when you save.
- The line under the list counts the listed types and, while you
  filter, the discovered types the filter shows: the ones `F5`, `F6`
  and `F8` change.

The modes:

- Work: `+` adds a type to Work, `-` removes it, a blank keeps Work's
  choice.
- Hide selected: every type is listed except the checked ones.
- Only selected: only the checked types are listed.
- All: every type is listed.

Each mode keeps its own marks: switching from Work to Only selected
starts with nothing checked, and switching back finds Work's marks.

A type added with `+` or checked in Only selected is listed even while
"Include empty" is No: its count shows `0`, `…` while kc counts it,
`denied` when you may not list it, or `n/a` when the count failed.
"Include empty" still applies to the other types.

## Keys

- `Up`, `Down`, `PgUp`, `PgDn`, `Home`, `End` move the cursor.
- `Space` changes the mark: in Work blank, `+`, `-`; in Hide selected
  and Only selected checked or not. `Insert` or `C-t` changes it and
  moves down, as marking files in mc does.
- Typing filters by name, API group, kind or short name; `Backspace`
  edits the filter; `Esc` clears it.
- `Tab`, `Shift-Tab` or `F4` switch the mode.
- `F5` Show (Work) or Check, `F6` Hide (Work) and `F8` Reset (Work) or
  Clear change every type the filter shows. They never touch a type
  that is not discovered.
- `F3` loads a saved profile into the editor.
- `Enter` applies the profile to this panel for this session.
- `F2` or `C-s` saves it, see below.
- `Esc`, or `F10`, closes without changes.
- A click on a row moves the cursor there; a click on the mark, or on
  the row under the cursor, changes the mark. A click on the mode
  switches it. The wheel moves the cursor.

## Save profile

The save dialog asks for a name: lower case letters, digits and `-`.
`all` is built in. `work` saves your changes to Work and must stay a
Work profile. The dialog suggests the profile's own name when the
profile is saved with the same mode, so `Enter` does not save one mode
over a profile of another.

- "Use for context NAME" makes the context use the profile. Clearing
  it for the profile the context uses removes that binding.
- "Use as the global default" makes it the default. Clearing it for the
  current default makes Work the default again.

Saving writes `resources.visibility` in `~/.kc/config.yaml`, and the
panel then lists under the saved profile. The other panel keeps what it
had until the context changes or kc starts again.

## Quick search

In a type list, quick search (`C-s`) also finds a type the list does
not show when no row matches the text: a type the profile leaves out,
or one with no row because it is empty or not accessible. The search
label turns the color of the `outside` mark and gives the reason, and
the details line names the type. `Enter` opens it, as "other types"
would; `C-s` goes to the next such type.

## Configuration

```yaml
resources:
  visibility:
    defaultProfile: platform
    profiles:
      platform:
        mode: work
        show:
          - group: databases.example.com
            resource: postgresclusters
        hide:
          - group: coordination.k8s.io
            resource: leases
      app:
        mode: only
        show:
          - group: ""
            resource: pods
          - group: apps
            resource: deployments
    contexts:
      production: app
```

`mode` is `work`, `hide`, `only` or `all`. A selector is an API group
(`""` for core) and a plural resource name, with no version and no
wildcards. `hide` takes no `show` list and `only` no `hide` list.

## Limits

- Profiles are not deleted or renamed in kc; edit
  `~/.kc/config.yaml`.
- A context binding matches the exact context name; there are no
  patterns.
- The editor reads the cached discovery: a type installed since then
  shows after the discovery refresh.

## See also

- [Resource types](resource-types.md)
- [Panel options](panel-options.md)
- [Menu bar](menu-bar.md)
