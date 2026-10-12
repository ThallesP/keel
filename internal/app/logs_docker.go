package app

import (
	"cmp"
	"encoding/binary"
	"slices"
	"strings"
	"time"

	"github.com/ThallesP/keel/internal/domain"
)

func parseDockerLine(raw, stream string) domain.ServiceLogLine {
	line := domain.ServiceLogLine{Text: raw, Stream: stream}
	if stamp, text, ok := strings.Cut(raw, " "); ok {
		if at, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
			line.Time, line.Text = float64(at.UnixMilli()), text
		}
	}
	details, text, _ := strings.Cut(line.Text, " ")
	if !strings.HasPrefix(details, "com.docker.swarm.") {
		return line
	}
	line.Text = text
	for _, kv := range strings.Split(details, ",") {
		if task, ok := strings.CutPrefix(kv, "com.docker.swarm.task.id="); ok {
			line.Task = task
			break
		}
	}
	return line
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

func demuxDockerLogs(buf []byte) []domain.ServiceLogLine {
	var stdout, stderr strings.Builder
	off := 0
	for off+8 <= len(buf) {
		typ := buf[off]
		n := int(binary.BigEndian.Uint32(buf[off+4 : off+8]))
		if typ > 2 || off+8+n > len(buf) {
			break
		}
		payload := buf[off+8 : off+8+n]
		if typ == 2 {
			stderr.Write(payload)
		} else {
			stdout.Write(payload)
		}
		off += 8 + n
	}
	if off == 0 && len(buf) > 0 {
		return splitDockerLines(string(buf), "stdout")
	}
	lines := append(splitDockerLines(stdout.String(), "stdout"), splitDockerLines(stderr.String(), "stderr")...)
	slices.SortStableFunc(lines, func(a, b domain.ServiceLogLine) int { return cmp.Compare(a.Time, b.Time) })
	return lines
}
