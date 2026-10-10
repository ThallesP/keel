package agent

import (
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

type Frame struct {
	Stream string
	Text   string
}

type FrameParser struct {
	carry []byte
}

func (p *FrameParser) Push(chunk []byte) []Frame {
	buf := append(p.carry, chunk...)
	var out []Frame
	off := 0
	for off+8 <= len(buf) {
		typ := buf[off]
		if typ > 2 {
			p.carry = nil
			return append(out, Frame{Stream: "stdout", Text: string(buf[off:])})
		}
		n := int(binary.BigEndian.Uint32(buf[off+4 : off+8]))
		if off+8+n > len(buf) {
			break
		}
		out = append(out, Frame{Stream: [...]string{"stdout", "stdout", "stderr"}[typ], Text: string(buf[off+8 : off+8+n])})
		off += 8 + n
	}
	p.carry = buf[off:]
	return out
}

type Line struct {
	Time   time.Time
	Text   string
	Stream string
}

type LineSplitter struct {
	partial map[string]string
}

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

func parseLine(raw, stream string) Line {
	line := strings.TrimSuffix(raw, "\r")
	stamp, text, ok := strings.Cut(line, " ")
	t, err := time.Parse(time.RFC3339Nano, stamp)
	if !ok || err != nil {
		return Line{Text: line, Stream: stream}
	}
	return Line{Time: t, Text: text, Stream: stream}
}

func dockerTime(t time.Time) string {
	return fmt.Sprintf("%d.%09d", t.Unix(), t.Nanosecond())
}
