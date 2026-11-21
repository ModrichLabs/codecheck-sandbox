// Package guest builds the guest root disk: it gathers the pinned inputs on
// the host, packs them for the builder VM, boots that VM through cc-launch,
// and installs the result.
package guest

import (
	"bytes"
	"fmt"
)

// newcEntry describes one file in a newc archive.
type newcEntry struct {
	name             string
	mode             uint32
	rdevMaj, rdevMin uint32
	data             []byte
}

// Initramfs packs init as the builder VM's /init, beside a /dev/console
// device node so the kernel can open the console before cc-guestinit mounts
// devtmpfs. Every entry belongs to root with a zero mtime, so the same init
// always gives the same bytes.
func Initramfs(init []byte) []byte {
	var buf bytes.Buffer
	for i, e := range []newcEntry{
		{name: "dev", mode: 0o040755},
		{name: "dev/console", mode: 0o020600, rdevMaj: 5, rdevMin: 1},
		{name: "init", mode: 0o100755, data: init},
		{name: "TRAILER!!!"},
	} {
		writeNewc(&buf, uint32(i+1), e)
	}
	return buf.Bytes()
}

// writeNewc writes one entry in the "070701" format: a header of thirteen
// 8-digit hex fields, the NUL-terminated name and the data, each padded to
// four bytes.
func writeNewc(buf *bytes.Buffer, ino uint32, e newcEntry) {
	nlink := uint32(1)
	if e.mode&0o170000 == 0o040000 {
		nlink = 2
	}
	if e.name == "TRAILER!!!" {
		ino, nlink = 0, 1
	}
	nameSize := len(e.name) + 1
	fmt.Fprintf(buf, "070701%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X",
		ino, e.mode, 0, 0, nlink, 0, len(e.data), 0, 0, e.rdevMaj, e.rdevMin, nameSize, 0)
	buf.WriteString(e.name)
	buf.WriteByte(0)
	pad(buf, 110+nameSize)
	buf.Write(e.data)
	pad(buf, len(e.data))
}

func pad(buf *bytes.Buffer, n int) {
	for range (4 - n%4) % 4 {
		buf.WriteByte(0)
	}
}
