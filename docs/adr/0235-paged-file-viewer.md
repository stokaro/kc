# 0235. Paged file viewer

- Status: Accepted
- Date: 2026-11-23
- PR: #0235

## Context

`F3` on a local file and on a container file read at most 512 KiB and dropped the rest without saying so. The viewer widget keeps the whole text in memory, highlights it with chroma and searches every line, so raising the limit would only move the problem. The user asked for buffered, paged reading, the way mc's viewer works.

## Decision

1. **Whole or paged.** A local file of up to 2 MiB and a container file of up to 512 KiB open whole, with syntax highlighting, as before. A larger file opens in the paged viewer. The open reads one byte past the limit, so a file that grew since it was listed opens in pages instead of being cut short. Links and special files in a container keep the old whole read. Directories, special local files, unreadable files and files with a NUL byte in the first 8000 bytes are refused as before; a container file is checked for the NUL byte when its first block arrives, and the viewer then says it is binary.

2. **A source read by offset.** `pagedfile.Source` has `ReadAt(ctx, p, off)` and `Size()`. A local file is an `*os.File`; `Size` stats it, so growth and shrinking are seen. A container file is read through the filesystem session the folder already holds: `podfs.RangeReader.ReadFileRange` runs `__kc_cat_range path offset length` in the session's shell, which is `stat -L -c %s` and `dd bs=4096 skip=… count=…` piped through `base64`. `dd` with `skip` and `count` works in busybox, GNU coreutils and the helper's busybox; it was checked with the busybox, Debian and Alpine images. Offsets are multiples of 4096, the `dd` block size. Every block read logs an INFO line with the namespace, pod, container, path, offset and length.

3. **API-server cost.** The filesystem session is one exec stream opened once per container. A block is one command on that stream, not a new exec request, and a key press reads a block only when the page moves into one that is not cached. Scrolling a page costs nothing once its block is cached; `End` reads one or two blocks at the end of the file; a search reads each block it passes once.

4. **Block cache.** Blocks are 256 KiB. At most 16 are kept (4 MiB), and the least recently used one goes first. The blocks of the visible lines are read first, then one block on either side. Reads run as Bubble Tea commands off the update loop; a block on its way shows as a `~ reading...` line, and moves that need it wait in a queue of at most 32 and run when it arrives. Closing the viewer cancels the reads and the search through the context they run under and closes the local file.

5. **Lines.** A line ends after a newline, or at an offset that is a multiple of 8 KiB with no newline in the 8 KiB before it. The rule needs only the bytes around an offset, so the same lines come out when the file is read from the top, from the end or from the middle, and a line is at most 16 KiB. A trailing newline does not add an empty last line, and `\r\n` shows as one line break. A split can fall inside a multi-byte character, which then shows as two broken characters; this only happens in lines of more than 8 KiB.

6. **Line numbers.** The line index is a counter state at each block boundary (about 32 bytes per 256 KiB), built while blocks arrive in order from the top. A line inside an indexed block is numbered by counting from the block start. `End` reads only the last blocks, so the lines before them are not counted: the status line shows the byte position instead (`~97%`). For a local file `End` and a search match start a background count of the whole file; after it the numbers are exact. A container file is never counted, since that reads the whole file through the pod. There is no go-to-line key; the viewer had none, and the index would support one.

7. **Search.** `F7` streams over the source from the top of the page forward (or from its bottom backward), one block at a time, with at most a few blocks in memory, and wraps around at the end. The footer shows the progress, `Esc` cancels the search through its context, and `n` and `N` search again from the active match. The match count of the whole-file viewer (`3/17`) is not known; the footer shows the line of the match, or its byte position.

8. **No highlighting in paged mode.** chroma needs the text from the start of the file to know the lexer state at a line, so highlighting a page in the middle would show wrong colors, and highlighting the whole file is what the paged mode avoids. The status line says `highlighting off`.

9. **F5.** The export of a container file streams the whole file through the session (`OpenContent`), so it is not cut at the viewer's limit. The 30-second copy timeout bounds a pause in the stream instead of the whole copy. `M-a` does not copy a paged file, which would hold it in memory; it says to use `F5`.

## Consequences

Files of any size open in a few blocks of memory, and the viewer answers keys while blocks arrive. Mouse selection, the match count and syntax highlighting are not available in paged mode. A file that changes while it is open shows its new content when the changed block is read again; `End` always reads the last block again.
