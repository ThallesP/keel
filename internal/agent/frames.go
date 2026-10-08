package agent

import (
	"encoding/binary"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Docker's multiplexed log stream: an 8-byte header [type, 0, 0, 0, len(u32 BE)] then len bytes
// of payload. Frames can split across reads, so the parser keeps a carry buffer. With the
// json-file / local drivers every frame is one log entry, but nothing guarantees the entry ends in
// "\n" (partial writes), so lines are split again per stream after reassembly
// (docs/go/spec/observability.md §11.5).

const (
	streamStdout = "stdout"
	streamStderr = "stderr"
)

// Frame is one demultiplexed chunk of a container's output.
type Frame struct {
	Stream string // "stdout" | "stderr"
	Text   string
}

// FrameParser demultiplexes Docker's stdout/stderr stream.
type FrameParser struct {
	carry []byte
}

// Push returns the whole frames decoded so far and keeps a trailing partial frame for the next
// call. A header whose type byte is above 2 means the container has a TTY (no multiplexing): the
// rest of the buffer is raw stdout text.
func (p *FrameParser) Push(chunk []byte) []Frame {
	buf := chunk
	if len(p.carry) > 0 {
		buf = make([]byte, 0, len(p.carry)+len(chunk))
		buf = append(append(buf, p.carry...), chunk...)
	}
	var out []Frame
	off := 0
	for off+8 <= len(buf) {
		typ := buf[off]
		n := int64(binary.BigEndian.Uint32(buf[off+4 : off+8]))
		if typ > 2 {
			out = append(out, Frame{Stream: streamStdout, Text: string(buf[off:])})
			p.carry = nil
			return out
		}
		if int64(off)+8+n > int64(len(buf)) {
			break
		}
		stream := streamStdout
		if typ == 2 {
			stream = streamStderr
		}
		out = append(out, Frame{Stream: stream, Text: string(buf[off+8 : off+8+int(n)])})
		off += 8 + int(n)
	}
	p.carry = append([]byte(nil), buf[off:]...)
	return out
}

// Line is one log line. Time is the RFC3339Nano prefix Docker adds with timestamps=1 ("" when
// the line had none).
type Line struct {
	Time   string
	Text   string
	Stream string
}

// LineSplitter splits frames into lines, keeping each stream's trailing partial line.
type LineSplitter struct {
	partial map[string]string
}

// Push returns the complete, non-empty lines of the frame.
func (s *LineSplitter) Push(f Frame) []Line {
	if s.partial == nil {
		s.partial = map[string]string{}
	}
	parts := strings.Split(s.partial[f.Stream]+f.Text, "\n")
	s.partial[f.Stream] = parts[len(parts)-1]
	var out []Line
	for _, raw := range parts[:len(parts)-1] {
		if raw != "" {
			out = append(out, parseLine(raw, f.Stream))
		}
	}
	return out
}

// parseLine strips one trailing "\r" and splits off the timestamp: the text before the first
// space is one when it is at least 20 characters, ends with "Z" and has "-" at index 4.
func parseLine(raw, stream string) Line {
	line := strings.TrimSuffix(raw, "\r")
	if space := strings.IndexByte(line, ' '); space > 0 {
		stamp := line[:space]
		if len(stamp) >= 20 && strings.HasSuffix(stamp, "Z") && stamp[4] == '-' {
			return Line{Time: stamp, Text: line[space+1:], Stream: stream}
		}
	}
	return Line{Text: line, Stream: stream}
}

var stampRE = regexp.MustCompile(`^(.+?)(?:\.(\d{1,9}))?Z$`)

// sinceAfter turns an RFC3339Nano stamp into Docker's `since` ("seconds.nanoseconds") plus one
// nanosecond, so the resume point is exclusive (Docker's since is inclusive). "" when the stamp
// does not parse.
func sinceAfter(stamp string) string {
	m := stampRE.FindStringSubmatch(stamp)
	if m == nil {
		return ""
	}
	t, err := time.Parse(time.RFC3339, m[1]+"Z")
	if err != nil {
		return ""
	}
	// Date.parse has millisecond precision; floor(ms / 1000) is the second of the stamp.
	secs := t.Unix()
	frac := m[2] + strings.Repeat("0", 9-len(m[2]))
	nanos, _ := strconv.ParseInt(frac, 10, 64)
	nanos++
	if nanos >= 1_000_000_000 {
		nanos -= 1_000_000_000
		secs++
	}
	return fmt.Sprintf("%d.%09d", secs, nanos)
}

// dockerSince turns epoch milliseconds (the sink's connect time, possibly fractional) into
// Docker's `since`: floor(ms/1000) "." pad9(min(round((ms % 1000) * 1e6), 999999999)).
func dockerSince(ms float64) string {
	nanos := math.Floor(math.Mod(ms, 1000)*1_000_000 + 0.5) // Math.round: half up
	nanos = math.Min(nanos, 999_999_999)
	return fmt.Sprintf("%d.%09d", int64(math.Floor(ms/1000)), int64(nanos))
}

// eventsSinceAfter is the events stream's resume point after an event: one nanosecond past its
// timeNano (since is inclusive), as "seconds.nanoseconds".
func eventsSinceAfter(timeNano int64) string {
	ns := timeNano + 1
	return fmt.Sprintf("%d.%09d", ns/1_000_000_000, ns%1_000_000_000)
}
