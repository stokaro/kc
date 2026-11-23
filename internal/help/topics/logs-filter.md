# Log Filter

`M-g` in the logs viewer opens the "Log filter" dialog. The filter hides
the lines you do not need and can pick fields out of JSON lines. It
changes only what is shown: the stream is not reopened, and clearing
the filter brings every kept line back.

## How it works

The filter is one line of terms separated by spaces. A line is shown
when it matches every term and no `!` term.

```
timeout             contains the text, in any case
"conn refused"      contains the quoted text
/5\d\d/             matches the regular expression (RE2)
level=error,warn    field equals one of the values, any case
id=="a,b C"         field is exactly the quoted value
msg~^GET            field matches the regular expression
!term               drops the lines that match term
.msg .http.status   shows only these fields of JSON lines
```

A regular expression ends at the slash before a space or the end, so it
may hold spaces and quotes. Inside `"..."` of an exact term, `\"` and
`\\` are the only escapes.

Field names are paths: `http.status`, `items[0].name`,
`["key with spaces"]`. An unquoted `http.status` also finds a literal
`http.status` key; a quoted `["a.b"]` is always that one key.

Field terms never match a text line. As an include they hide text
lines, as an exclude they keep them. The status line shows `N of M
lines`, so nothing disappears silently.

The `.field` terms work in the formatted view on JSON lines. A line
without any of the fields is shown whole.

## JSON lines

A line is JSON when, after an optional timestamp from `--timestamps`, it
is exactly one JSON object. The formatted view shows the time (dimmed),
the level (upper case, colored by severity), the message, then the
other fields as `key=value`, nested objects as dotted keys. Arrays,
broken JSON and lines over 256 KiB stay text. `M-v` switches to the raw
lines; the filter still applies there, field extraction does not.

In a merged log the `[pod/container]` prefix stays in front in both
views, and the JSON after it is parsed.

## Keys
- `Enter` applies the filter and closes the dialog. An empty text
  clears the filter.
- `Esc` closes the dialog without changes.
- An invalid term is reported under the field, and the dialog stays
  open.
- In the viewer, `M-a` copies the shown lines, `M-A` the original
  records that pass the filter, without the merged log's prefix.

## Limits
- The filter works on the lines the viewer keeps. Lines dropped to stay
  within `viewer.logs.maxLines` are not searched.
- The filter belongs to this viewer. It is not saved, and it survives
  an `F2` change of the stream options.
- The Logs panel has no filter; open the full-screen viewer.

## See also
- [Logs viewer](logs-viewer.md)
- [Log fields](logs-fields.md)
