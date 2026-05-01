package resp

import (
	"fmt"
	"strings"
)

// lolwutCmd implements LOLWUT [VERSION n] — Redis's joke command
// that prints a pixel-art Redis logo plus the server version. Real
// Redis renders different art per VERSION arg; we ship a single
// terminal-friendly NeuroCache banner so monitoring tools that
// poke LOLWUT to verify "the server speaks Redis" see a sensible
// reply instead of an unknown-command error.
//
// The arg is accepted (any VERSION value) and ignored.
func (c *conn) lolwutCmd(_ []string) {
	var b strings.Builder
	for _, line := range banner {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "\nNeuroCache v%s — Redis-compatible AI-native data store\n", neuroCacheVersion)
	writeBulk(c.bw, b.String())
}

// banner is the ascii-art rendered by LOLWUT. Compact and ASCII-
// only (no extended chars) so terminals without UTF-8 don't spew
// replacement glyphs.
var banner = []string{
	`                                                  `,
	`           _   _                                  `,
	`          | \ | |                                 `,
	`          |  \| | ___ _   _ _ __ ___              `,
	`          | . ` + "`" + ` |/ _ \ | | | '__/ _ \             `,
	`          | |\  |  __/ |_| | | | (_) |            `,
	`          |_| \_|\___|\__,_|_|  \___/             `,
	`        ___           _                           `,
	`       / __\__ _  ___| |__   ___                  `,
	`      / /  / _` + "`" + ` |/ __| '_ \ / _ \                 `,
	`     / /__| (_| | (__| | | |  __/                 `,
	`     \____/\__,_|\___|_| |_|\___|                 `,
	`                                                  `,
}

// neuroCacheVersion is the build-time version surfaced to LOLWUT
// and INFO replies. Wired here so a build script can use -ldflags
// '-X resp.neuroCacheVersion=...' to inject the real semver
// without touching this file.
var neuroCacheVersion = "0.5.0"
