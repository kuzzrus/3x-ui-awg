package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
)

type sampleBody struct {
	Port     int    `json:"port" form:"port" validate:"gte=1,lte=65535"`
	Protocol string `json:"protocol" form:"protocol" validate:"required,oneof=vmess vless trojan"`
	Tag      string `json:"tag" form:"tag"`
}

func newRouter(handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/submit", handler)
	return r
}

func decodeMsg(t *testing.T, body string) entity.Msg {
	t.Helper()
	var msg entity.Msg
	if err := json.Unmarshal([]byte(body), &msg); err != nil {
		t.Fatalf("decode msg: %v (body=%q)", err, body)
	}
	return msg
}

func TestBindAndValidate_ValidPayloadPassesThrough(t *testing.T) {
	r := newRouter(func(c *gin.Context) {
		got, ok := BindAndValidate[sampleBody](c)
		if !ok {
			t.Fatalf("expected ok=true, got false (body should be valid)")
		}
		if got.Port != 443 || got.Protocol != "vless" || got.Tag != "inbound-443" {
			t.Fatalf("decoded payload mismatch: %+v", got)
		}
		c.JSON(http.StatusOK, entity.Msg{Success: true, Msg: "ok"})
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/submit",
		strings.NewReader(`{"port":443,"protocol":"vless","tag":"inbound-443"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body=%s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if msg := decodeMsg(t, rec.Body.String()); !msg.Success {
		t.Fatalf("expected Success=true; got %+v", msg)
	}
}

func TestBindAndValidate_PortOutOfRangeIsRejected(t *testing.T) {
	r := newRouter(func(c *gin.Context) {
		if _, ok := BindAndValidate[sampleBody](c); ok {
			t.Fatal("expected ok=false on invalid port; got true")
		}
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/submit",
		strings.NewReader(`{"port":70000,"protocol":"vless"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	msg := decodeMsg(t, rec.Body.String())
	if msg.Success {
		t.Fatalf("expected Success=false; got %+v", msg)
	}
	payload, err := payloadFromObj(msg.Obj)
	if err != nil {
		t.Fatalf("payload extraction: %v", err)
	}
	found := false
	for _, issue := range payload.Issues {
		if issue.Field == "port" && issue.Rule == "lte" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected an Issue for field=port rule=lte; got %+v", payload.Issues)
	}
}

func TestBindAndValidate_ProtocolEnumIsRejected(t *testing.T) {
	r := newRouter(func(c *gin.Context) {
		if _, ok := BindAndValidate[sampleBody](c); ok {
			t.Fatal("expected ok=false on invalid protocol; got true")
		}
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/submit",
		strings.NewReader(`{"port":443,"protocol":"unknown"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	msg := decodeMsg(t, rec.Body.String())
	payload, err := payloadFromObj(msg.Obj)
	if err != nil {
		t.Fatalf("payload extraction: %v", err)
	}
	found := false
	for _, issue := range payload.Issues {
		if issue.Field == "protocol" && issue.Rule == "oneof" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an Issue for field=protocol rule=oneof; got %+v", payload.Issues)
	}
}

func TestBindAndValidate_MalformedJSONReturnsMessageButNoIssues(t *testing.T) {
	r := newRouter(func(c *gin.Context) {
		if _, ok := BindAndValidate[sampleBody](c); ok {
			t.Fatal("expected ok=false on malformed JSON; got true")
		}
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/submit",
		strings.NewReader(`{"port":}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	msg := decodeMsg(t, rec.Body.String())
	if msg.Success {
		t.Fatal("expected Success=false on malformed JSON")
	}
	payload, err := payloadFromObj(msg.Obj)
	if err != nil {
		t.Fatalf("payload extraction: %v", err)
	}
	if len(payload.Issues) != 0 {
		t.Fatalf("expected empty Issues for parse error; got %+v", payload.Issues)
	}
	if payload.Message == "" {
		t.Fatal("expected non-empty Message describing the parse error")
	}
}

func TestBindAndValidateInto_PreservesPrePopulatedFields(t *testing.T) {
	r := newRouter(func(c *gin.Context) {
		dst := &sampleBody{Tag: "preset"}
		if !BindAndValidateInto(c, dst) {
			t.Fatal("expected ok=true; got false")
		}
		if dst.Tag != "inbound-443" {
			t.Fatalf("expected payload Tag to overwrite preset; got %q", dst.Tag)
		}
		if dst.Port != 443 {
			t.Fatalf("expected Port=443; got %d", dst.Port)
		}
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/submit",
		strings.NewReader(`{"port":443,"protocol":"trojan","tag":"inbound-443"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestBindJSONAndValidate_RejectsFormEncodedBody(t *testing.T) {
	r := newRouter(func(c *gin.Context) {
		if _, ok := BindJSONAndValidate[sampleBody](c); ok {
			t.Fatal("expected ok=false for form-encoded request to a JSON-only endpoint")
		}
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/submit",
		strings.NewReader("port=443&protocol=vless"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(rec, req)

	if msg := decodeMsg(t, rec.Body.String()); msg.Success {
		t.Fatalf("expected Success=false; got %+v", msg)
	}
}

type optionalBody struct {
	Pull *bool  `json:"pull" form:"pull"`
	Port int    `json:"port" form:"port" validate:"omitempty,gte=1,lte=65535"`
	Url  string `json:"url" form:"url"`
}

func postOptional(t *testing.T, contentType, body string, check func(got *optionalBody, ok bool)) *httptest.ResponseRecorder {
	t.Helper()
	r := newRouter(func(c *gin.Context) {
		got, ok := BindOptionalAndValidate[optionalBody](c)
		check(got, ok)
		if ok {
			c.JSON(http.StatusOK, entity.Msg{Success: true})
		}
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/submit", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	r.ServeHTTP(rec, req)
	return rec
}

func TestBindOptionalAndValidate_NoBodyIsARequestWithNoFields(t *testing.T) {
	for name, tc := range map[string]struct{ contentType, body string }{
		"json with nothing in it":   {"application/json", ""},
		"json with only a newline":  {"application/json", " \n"},
		"a form with nothing in it": {"application/x-www-form-urlencoded", ""},
		"no content type at all":    {"", ""},
	} {
		t.Run(name, func(t *testing.T) {
			rec := postOptional(t, tc.contentType, tc.body, func(got *optionalBody, ok bool) {
				if !ok || got == nil || *got != (optionalBody{}) {
					t.Fatalf("got %+v, ok=%v, want a request with no fields", got, ok)
				}
			})
			if msg := decodeMsg(t, rec.Body.String()); !msg.Success {
				t.Fatalf("expected Success=true; got %+v", msg)
			}
		})
	}
}

func TestBindOptionalAndValidate_ABodyIsBoundAndValidatedAsUsual(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		postOptional(t, "application/json", `{"pull":true,"port":9443,"url":"https://panel.example.com/"}`, func(got *optionalBody, ok bool) {
			if !ok || got.Pull == nil || !*got.Pull || got.Port != 9443 || got.Url != "https://panel.example.com/" {
				t.Fatalf("got %+v, ok=%v, want the fields of the body", got, ok)
			}
		})
	})
	t.Run("a form", func(t *testing.T) {
		postOptional(t, "application/x-www-form-urlencoded", "pull=false&port=9443", func(got *optionalBody, ok bool) {
			if !ok || got.Pull == nil || *got.Pull || got.Port != 9443 {
				t.Fatalf("got %+v, ok=%v, want the fields of the form", got, ok)
			}
		})
	})
	for name, body := range map[string]string{"a field out of range": `{"port":70000}`, "malformed json": `{"port":`} {
		t.Run(name, func(t *testing.T) {
			rec := postOptional(t, "application/json", body, func(got *optionalBody, ok bool) {
				if ok {
					t.Fatalf("a body that is not valid was taken: %+v", got)
				}
			})
			if msg := decodeMsg(t, rec.Body.String()); msg.Success {
				t.Fatalf("expected Success=false; got %+v", msg)
			}
		})
	}
}

func TestBindOptionalAndValidate_NoBodyStillHasToPassValidation(t *testing.T) {
	r := newRouter(func(c *gin.Context) {
		if _, ok := BindOptionalAndValidate[sampleBody](c); ok {
			t.Fatal("a request with no body was taken for a type with required fields")
		}
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/submit", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	if msg := decodeMsg(t, rec.Body.String()); msg.Success {
		t.Fatalf("expected Success=false; got %+v", msg)
	}
}

func payloadFromObj(obj any) (ValidationPayload, error) {
	raw, err := json.Marshal(obj)
	if err != nil {
		return ValidationPayload{}, err
	}
	var payload ValidationPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ValidationPayload{}, err
	}
	return payload, nil
}
