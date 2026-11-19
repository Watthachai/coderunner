package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Watthachai/fitt-coderunner/internal/domain"
)

// payloadJobs records the payload and options of each edit it is handed.
type payloadJobs struct {
	rehearsalJobs
	payloads [][]byte
}

func (j *payloadJobs) Enqueue(ctx context.Context, p, o uuid.UUID, payload []byte) (*domain.Job, error) {
	j.payloads = append(j.payloads, payload)
	return j.rehearsalJobs.Enqueue(ctx, p, o, payload)
}

func (j *payloadJobs) EnqueueWith(ctx context.Context, p, o uuid.UUID, payload []byte, opts domain.EnqueueOptions) (*domain.Job, error) {
	j.payloads = append(j.payloads, payload)
	job, err := j.rehearsalJobs.EnqueueWith(ctx, p, o, payload, opts)
	if job != nil && job.ID == uuid.Nil {
		job.ID = uuid.New()
	}
	return job, err
}

func attachmentServer() (*server, *payloadJobs, uuid.UUID) {
	s, _, _, pid := rehearsalServer()
	jm := &payloadJobs{}
	s.jm = jm
	return s, jm, pid
}

var (
	shotPNG  = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, 64)...)
	shotJPEG = append([]byte{0xFF, 0xD8, 0xFF, 0xE1}, bytes.Repeat([]byte{8}, 64)...)
)

func picture(b []byte, mime, name string, item int) map[string]any {
	s := sha256.Sum256(b)
	p := map[string]any{"name": name, "mime": mime, "sha256": hex.EncodeToString(s[:]), "bytes": base64.StdEncoding.EncodeToString(b)}
	if item > 0 {
		p["item"] = item
	}
	return p
}

const envelope = "FITTCORE-CHANGE-REQUEST v1 boundary=b1\n--b1 item=1 source=operator\nThe red box\n" +
	"--b1 item=2 source=operator\nThe pin\n--b1 end=2\n"

func postEdit(t *testing.T, s *server, pid uuid.UUID, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.handleEditProject(w, withID(httptest.NewRequest("POST", "/internal/projects/x/edit", bytes.NewReader(raw)), pid))
	return w
}

// The pictures go to the job manager whole; the payload names them without the bytes; the
// 202 says how many CRN took and the sha256 of each, so the sender can keep the receipt.
func TestEditTakesPicturesAndAnswersWithWhatItGot(t *testing.T) {
	s, jm, pid := attachmentServer()
	w := postEdit(t, s, pid, map[string]any{
		"change":      envelope,
		"attachments": []any{picture(shotPNG, "image/png", "pin-1.png", 2), picture(shotJPEG, "image/jpeg", "all.jpg", 0)},
	})
	if w.Code != 202 {
		t.Fatalf("edit with pictures: %d %s", w.Code, w.Body.String())
	}

	if len(jm.with) != 1 || len(jm.with[0].Attachments) != 2 {
		t.Fatalf("enqueue options = %+v, want the two pictures", jm.with)
	}
	got := jm.with[0].Attachments
	if !bytes.Equal(got[0].Data, shotPNG) || got[0].Item != 2 || got[0].File != "01.png" || !bytes.Equal(got[1].Data, shotJPEG) || got[1].Item != 0 {
		t.Fatalf("pictures handed on = %+v", got)
	}

	payload := string(jm.payloads[0])
	if strings.Contains(payload, base64.StdEncoding.EncodeToString(shotPNG)) || strings.Contains(payload, `"bytes"`) {
		t.Fatalf("the payload holds the picture itself: %s", payload)
	}
	var p struct {
		Attachments []domain.Attachment `json:"attachments"`
	}
	if err := json.Unmarshal(jm.payloads[0], &p); err != nil || len(p.Attachments) != 2 || p.Attachments[0].SHA256 != got[0].SHA256 || p.Attachments[0].File != "01.png" || p.Attachments[1].Size != len(shotJPEG) {
		t.Fatalf("payload attachments = %+v (%v)", p.Attachments, err)
	}

	var resp struct {
		Received    int `json:"attachments_received"`
		Attachments []struct {
			Item   int    `json:"item"`
			Name   string `json:"name"`
			SHA256 string `json:"sha256"`
			Size   int    `json:"size"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Received != 2 || len(resp.Attachments) != 2 ||
		resp.Attachments[0].SHA256 != got[0].SHA256 || resp.Attachments[0].Item != 2 || resp.Attachments[0].Name != "pin-1.png" ||
		resp.Attachments[1].SHA256 != got[1].SHA256 || resp.Attachments[1].Size != len(shotJPEG) {
		t.Fatalf("202 = %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), base64.StdEncoding.EncodeToString(shotPNG)) {
		t.Fatal("the 202 sends the picture back")
	}
}

// An edit without pictures says it took none, and goes on as before.
func TestEditWithoutPicturesSaysItTookNone(t *testing.T) {
	s, jm, pid := attachmentServer()
	w := postEdit(t, s, pid, map[string]any{"change": "x"})
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"attachments_received":0`) {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	if jm.plain != 1 || len(jm.with) != 0 || strings.Contains(string(jm.payloads[0]), "attachments") {
		t.Fatalf("an edit without pictures changed: plain %d, with %+v, payload %s", jm.plain, jm.with, jm.payloads[0])
	}
}

// One picture that is not right refuses the request: 400, the reason, and no job.
func TestEditRefusesTheRequestOverOneWrongPicture(t *testing.T) {
	s, jm, pid := attachmentServer()
	bad := picture(shotJPEG, "image/jpeg", "b.jpg", 0)
	bad["sha256"] = strings.Repeat("0", 64)
	w := postEdit(t, s, pid, map[string]any{"change": envelope, "attachments": []any{picture(shotPNG, "image/png", "a.png", 1), bad}})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "attachments[1]: sha256 does not match the bytes") {
		t.Fatalf("edit with a wrong picture: %d %s", w.Code, w.Body.String())
	}
	if jm.plain != 0 || len(jm.with) != 0 {
		t.Fatal("a refused request was queued")
	}

	w = postEdit(t, s, pid, map[string]any{"change": envelope, "attachments": []any{picture(shotPNG, "image/png", "a.png", 5)}})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "attachments[0]: item 5 is not an item of the change (it has items 1, 2)") {
		t.Fatalf("edit naming an item the change lacks: %d %s", w.Code, w.Body.String())
	}

	w = postEdit(t, s, pid, map[string]any{"change": "x", "attachments": []any{map[string]any{"name": "a", "mime": "image/png", "sha256": "", "bytes": "", "colour": "red"}}})
	if w.Code != 400 || !strings.Contains(w.Body.String(), `unknown field \"colour\"`) {
		t.Fatalf("edit with a field the contract lacks: %d %s", w.Code, w.Body.String())
	}
}

// A body past 32 MiB is refused before it is read to the end.
func TestEditRefusesABodyPastThirtyTwoMiB(t *testing.T) {
	s, jm, pid := attachmentServer()
	body := `{"change":"x","attachments":[{"name":"a.png","mime":"image/png","sha256":"","bytes":"` +
		strings.Repeat("A", 33<<20) + `"}]}`
	w := httptest.NewRecorder()
	s.handleEditProject(w, withID(httptest.NewRequest("POST", "/internal/projects/x/edit", strings.NewReader(body)), pid))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "request body is larger than 32 MiB") {
		t.Fatalf("a 33 MiB body: %d %s", w.Code, w.Body.String())
	}
	if jm.plain != 0 || len(jm.with) != 0 {
		t.Fatal("an oversized request was queued")
	}
}
