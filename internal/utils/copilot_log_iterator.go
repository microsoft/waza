package utils

import (
	"encoding/json"
	"errors"
	"io"
	"iter"
	"os"

	copilot "github.com/github/copilot-sdk/go"
)

// NewCopilotLogIterator creates an iterator for an events.jsonl formatted log
func NewCopilotLogIterator(path string) iter.Seq2[copilot.SessionEvent, error] {
	return func(yield func(copilot.SessionEvent, error) bool) {
		stopped := false

		err := func() (err error) {
			reader, err := os.Open(path)
			if err != nil {
				return err
			}
			defer func() {
				err = errors.Join(err, reader.Close())
			}()

			decoder := json.NewDecoder(reader)

			for {
				var event *copilot.SessionEvent

				if err := decoder.Decode(&event); err != nil {
					if errors.Is(err, io.EOF) {
						break
					}
					return err
				}

				if !yield(*event, nil) {
					stopped = true
					return nil
				}
			}
			return nil
		}()

		if !stopped && err != nil {
			_ = yield(copilot.SessionEvent{}, err)
		}
	}
}
