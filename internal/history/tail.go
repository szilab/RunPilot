package history

import (
	"bufio"
	"os"
)

func Tail(path string, maxLines int) (string, error) {
	if maxLines <= 0 {
		maxLines = 200
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	lines := make([]string, 0, maxLines)
	sc := bufio.NewScanner(f)
	buf := make([]byte, 64*1024)
	sc.Buffer(buf, 2*1024*1024)
	for sc.Scan() {
		if len(lines) == maxLines {
			copy(lines, lines[1:])
			lines[len(lines)-1] = sc.Text()
		} else {
			lines = append(lines, sc.Text())
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out, nil
}
