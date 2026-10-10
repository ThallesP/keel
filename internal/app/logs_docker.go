package app

import (
	"encoding/binary"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ThallesP/keel/internal/domain"
)

const dockerTaskKey = "com.docker.swarm.task.id="

func parseDockerLine(raw, stream string) domain.ServiceLogLine {
	rest := raw
	t := 0.0
	stamp, text, hasText := strings.Cut(raw, " ")
	if at, err := time.Parse(time.RFC3339Nano, stamp); hasText && err == nil {
		t, rest = float64(at.UnixMilli()), text
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

func demuxDockerLogs(buf []byte) []domain.ServiceLogLine {
	var stdout, stderr strings.Builder
	off := 0
	for off+8 <= len(buf) {
		typ := buf[off]
		n := int(binary.BigEndian.Uint32(buf[off+4 : off+8]))
		if typ > 2 || off+8+n > len(buf) || off+8+n < off {
			break
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
