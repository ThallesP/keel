package app

// Docker's service log body → lines (logProviders/docker.ts demux and parseLine).

import (
	"encoding/binary"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ThallesP/keel/internal/domain"
)

const dockerTaskKey = "com.docker.swarm.task.id="

var dockerStampMsRE = regexp.MustCompile(`(\.[0-9]{3})[0-9]+Z$`)

// parseDockerLine: `2026-09-14T04:05:06.123456789Z com.docker.swarm.node.id=…,com.docker.swarm.
// task.id=… text` → time, task, text. The details block exists only with details=1, the stamp
// only with timestamps=1. Lines without a stamp keep time 0.
func parseDockerLine(raw, stream string) domain.ServiceLogLine {
	rest := raw
	t := 0.0
	space := strings.IndexByte(rest, ' ')
	stamp := ""
	if space > 0 {
		stamp = rest[:space]
	}
	if strings.HasSuffix(stamp, "Z") {
		if ms, ok := jsDateParse(dockerStampMsRE.ReplaceAllString(stamp, "${1}Z")); ok {
			t = ms
			rest = rest[space+1:]
		}
	}
	task := ""
	detailsEnd := strings.IndexByte(rest, ' ')
	details := rest
	if detailsEnd > 0 {
		details = rest[:detailsEnd]
	}
	if strings.HasPrefix(details, "com.docker.swarm.") {
		for _, kv := range strings.Split(details, ",") {
			if strings.HasPrefix(kv, dockerTaskKey) {
				task = kv[len(dockerTaskKey):]
				break
			}
		}
		if detailsEnd > 0 {
			rest = rest[detailsEnd+1:]
		} else {
			rest = ""
		}
	}
	return domain.ServiceLogLine{Time: t, Text: rest, Stream: stream, Task: task}
}

// splitDockerLines drops empty lines, then strips one trailing \r (in that order, as the TS did).
func splitDockerLines(text, stream string) []domain.ServiceLogLine {
	out := []domain.ServiceLogLine{}
	for _, l := range strings.Split(text, "\n") {
		if l == "" {
			continue
		}
		out = append(out, parseDockerLine(strings.TrimSuffix(l, "\r"), stream))
	}
	return out
}

// dockerUTF8 is Buffer.toString("utf8"): invalid bytes become U+FFFD.
func dockerUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		sb.WriteRune(r)
		b = b[size:]
	}
	return sb.String()
}

// demuxDockerLogs: non-TTY Docker logs are 8-byte frame headers [type,0,0,0,len u32 BE] followed
// by the payload. A body with no frame is a TTY service's plain text (all stdout). Lines are
// stdout's then stderr's, stable-sorted by time.
func demuxDockerLogs(buf []byte) []domain.ServiceLogLine {
	var stdout, stderr strings.Builder
	off := 0
	for off+8 <= len(buf) {
		typ := buf[off]
		n := int(binary.BigEndian.Uint32(buf[off+4 : off+8]))
		if typ > 2 || off+8+n > len(buf) || off+8+n < off {
			break // not multiplexed (TTY) or truncated
		}
		payload := dockerUTF8(buf[off+8 : off+8+n])
		if typ == 2 {
			stderr.WriteString(payload)
		} else {
			stdout.WriteString(payload)
		}
		off += 8 + n
	}
	if off == 0 && len(buf) > 0 {
		return splitDockerLines(dockerUTF8(buf), "stdout")
	}
	lines := append(splitDockerLines(stdout.String(), "stdout"), splitDockerLines(stderr.String(), "stderr")...)
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Time < lines[j].Time })
	return lines
}
