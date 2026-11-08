package broker

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ivy/almanac/diag"
)

// RecordedExt is the extension of a recorded delivery's file.
const RecordedExt = ".delivery"

// RecordedFormat describes a recorded delivery, for the replay command's help.
const RecordedFormat = `A recorded delivery is one file named NAME` + RecordedExt + `: two header lines, a
blank line, then the body exactly as GitHub sent it, byte for byte, with nothing
added after it:

    X-GitHub-Event: pull_request
    X-GitHub-Delivery: 72d3162e-cc78-11e3-81ab-4c9367dc0958

    {"action":"opened",...}

Header names are case-insensitive, and lines may end in LF or CRLF. No other
header is allowed: the signature is computed when replaying. Files are replayed
in name order, so name them by their place in the log, such as
000042-72d3162e.delivery. Other files in the directory are ignored.`

// Recorded is one delivery read from a file, ready to be signed and sent again.
type Recorded struct {
	Event    string
	Delivery string
	Body     []byte
}

// ReadRecorded reads a recorded delivery. See [RecordedFormat].
func ReadRecorded(path string) (Recorded, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Recorded{}, diag.UnreadableInput(path, err)
	}
	reader := bufio.NewReader(bytes.NewReader(data))
	header, err := textproto.NewReader(reader).ReadMIMEHeader()
	if err != nil {
		return Recorded{}, notRecorded(path, "its headers do not end in a blank line")
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return Recorded{}, diag.UnreadableInput(path, err)
	}
	r := Recorded{
		Event:    header.Get("X-GitHub-Event"),
		Delivery: header.Get("X-GitHub-Delivery"),
		Body:     body,
	}
	for name := range header {
		if name != "X-Github-Event" && name != "X-Github-Delivery" {
			return Recorded{}, notRecorded(path, "it has a header other than X-GitHub-Event and X-GitHub-Delivery: "+name)
		}
	}
	if r.Event == "" || r.Delivery == "" {
		return Recorded{}, notRecorded(path, "it needs both X-GitHub-Event and X-GitHub-Delivery")
	}
	return r, nil
}

func notRecorded(path, why string) error {
	return fmt.Errorf("%s is not a recorded delivery, because %s; write it in the format `almanac-broker replay --help` describes", path, why)
}

// Replay signs each recorded delivery in dir with secret and posts it to url,
// a broker's webhook endpoint, in name order. It writes a line per delivery
// to out, and fails if any delivery was not accepted.
func Replay(ctx context.Context, client *http.Client, dir, url, secret string, out io.Writer) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return diag.UnreadableInput(dir, err)
	}
	var names []string
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), RecordedExt) {
			names = append(names, entry.Name())
		}
	}
	slices.Sort(names)
	if len(names) == 0 {
		return fmt.Errorf("%s holds no %s files, so there is nothing to replay; record deliveries there first", dir, RecordedExt)
	}
	refused := 0
	for _, name := range names {
		recorded, err := ReadRecorded(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		status, err := post(ctx, client, url, secret, recorded)
		if err != nil {
			return fmt.Errorf("cannot post %s to %s: %w; check that the broker is running and the URL ends in /webhooks/github", name, url, err)
		}
		if _, err := fmt.Fprintf(out, "%s: %d %s\n", name, status, http.StatusText(status)); err != nil {
			return err
		}
		if status/100 != 2 {
			refused++
		}
	}
	if refused > 0 {
		return fmt.Errorf("the broker refused %d of %d deliveries; a 401 means ALMANAC_WEBHOOK_SECRET differs from the broker's", refused, len(names))
	}
	return nil
}

func post(ctx context.Context, client *http.Client, url, secret string, r Recorded) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(r.Body))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "almanac-broker-replay")
	request.Header.Set("X-GitHub-Event", r.Event)
	request.Header.Set("X-GitHub-Delivery", r.Delivery)
	request.Header.Set("X-Hub-Signature-256", Sign(secret, r.Body))
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	return response.StatusCode, nil
}
