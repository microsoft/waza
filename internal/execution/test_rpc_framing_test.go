package execution

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

func readTestRPCFrame(reader *bufio.Reader) ([]byte, error) {
	length := 0
	for {
		header, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if header == "\r\n" {
			break
		}
		if after, ok := strings.CutPrefix(header, "Content-Length: "); ok {
			length, err = strconv.Atoi(strings.TrimSpace(after))
			if err != nil {
				return nil, err
			}
		}
	}
	if length <= 0 || length > 1<<20 {
		return nil, fmt.Errorf("invalid frame length %d", length)
	}
	body := make([]byte, length)
	_, err := io.ReadFull(reader, body)
	return body, err
}

func writeTestRPCFrame(writer io.Writer, response any) error {
	encoded, err := json.Marshal(response)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, "Content-Length: %d\r\n\r\n%s", len(encoded), encoded)
	return err
}
