# 0066. ConfigMap binary data keys

- Status: Accepted
- Date: 2026-11-22
- Issue: #66

## Context

A ConfigMap stores UTF-8 strings in `data` and arbitrary bytes, base64
encoded, in `binaryData`. The API server refuses a key present in both. kc
listed only `data`, so a ConfigMap with only `binaryData` showed an empty
data folder.

The issue leaves open how F3 shows a binary key, how the list marks text
and binary keys, and how F5 keeps the bytes exact. F3 and F5 share one
source: the item's `ViewContent`, whose body the viewer shows and F5
writes as `[]byte(body)`. Secret keys already decode their values and mark
binary values with the MIME type `application/octet-stream`, but the
viewer ignored that type and wrote the bytes to the terminal.

## Decision

1. **List.** The ConfigMap data folder merges `data` and `binaryData`,
   sorted by key, with the columns Name, Type and Size. Type is `text` for
   a `data` key and `binary` for a `binaryData` key, so it names the field,
   not a guess about the content. Size is the value's byte count (decoded
   for `binaryData`).

2. **One contract for binary content.** `ViewContent` keeps returning the
   exact value as the body. A Go string holds arbitrary bytes, so F5 writes
   the original bytes, including NUL and invalid UTF-8. The MIME type
   `application/octet-stream` marks a body as raw bytes. `binaryData` keys
   always carry it. A `data` value carries it when it is not safe text:
   invalid UTF-8 (impossible for `data`, but shared with Secrets), C0
   controls other than tab, CR and LF, DEL, or C1 controls (0x9b is CSI).

3. **F3 shows a summary and a bounded hex dump.** When the viewer gets
   `application/octet-stream` content, it replaces the body with plain
   ASCII: size, the type detected by `http.DetectContentType`, the SHA-256
   (to check an exported file against), and a `hexdump -C` style dump of
   the first 4 KiB. The rest is counted, not shown. This is done in the
   viewer widget, so the F3 modal and the manifest panel behave the same,
   and Secret binary keys get the same treatment.

4. **Copied commands.** A text key prints
   `kubectl get configmap NAME -n NS -o 'jsonpath={.data.KEY}'`; a binary
   key prints `... 'jsonpath={.binaryData.KEY}' | base64 -d`, as Secret
   keys do. Dots in keys are escaped. The folder command prints both
   fields: `jsonpath={.data}{"\n"}{.binaryData}`.

## Alternatives considered

- **A separate export interface for F5** (an item method returning
  `[]byte`), with F3 getting a preview body. It adds API surface and a
  second read path for one case. Keeping one body and marking it by MIME
  type reuses the contract Secrets already had.
- **Refuse F3 on binary keys** with only a message. Safe, but the size,
  type and first bytes are what a user usually needs to identify a file.
- **Escape the bytes as text** (`\x00`, `\xff`). Readable for short
  values, but a long blob becomes one unreadable line and is easy to
  mistake for the real content.
- **Decide text versus binary by content for the Type column.** The field
  is what kubectl and the API use; showing it keeps the list honest about
  where the key lives and how the copied command reads it.
- **A marker in the Name cell** (such as a trailing `*`). It changes the
  name a user may type or search for; a separate column does not.

## Consequences

- Secret binary keys now show the hex dump in F3 too. Secret values that
  are invalid UTF-8 or contain DEL or C1 controls, which were shown as
  text before, are now treated as binary in F3. F5 is unchanged for them.
- A `data` value containing an escape sequence is listed as `text` but
  opens as a hex dump. This is deliberate: the bytes would otherwise drive
  the terminal.
- The Secret data folder has no Type or Size column yet. Adding them is a
  small follow-up.
- F5 relies on the body staying unchanged on the way to disk. A test pins
  that the copy request writes the body byte for byte.
