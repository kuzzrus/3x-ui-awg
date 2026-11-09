package controller

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/web/locale"
)

// sidecarEngine serves the Xray setting routes the way the panel does, minus the session: the i18n function answers
// with the key. before are middleware that run ahead of the routes.
func sidecarEngine(before ...gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("I18n", func(_ locale.I18nType, key string, _ ...string) string { return key })
		c.Next()
	})
	engine.Use(before...)
	NewXraySettingController(engine.Group("/panel/api"))
	return engine
}

func postTo(engine *gin.Engine, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
	return w
}

// Most of these switches on the action had no default case, so a request for an action that does not exist came back as
// a success; adguard even answered it with the status.
func TestSidecarEndpointsRefuseAnUnknownAction(t *testing.T) {
	engine := sidecarEngine()
	for _, path := range []string{
		"warp/bogus", "nord/bogus", "tor/bogus", "psiphon/bogus", "psiphon/countries/bogus", "psiphon/scan/bogus",
		"naiveproxy/bogus", "wireproxy/bogus", "frontproxy/bogus", "adguard/bogus", "pia/bogus",
	} {
		t.Run(path, func(t *testing.T) {
			body := postTo(engine, "/panel/api/xray/"+path).Body.String()
			if !strings.Contains(body, `"success":false`) || !strings.Contains(body, "unknown action") {
				t.Errorf("POST %s answered %s, want a failure that says unknown action", path, body)
			}
		})
	}
}

// deadlineRecorder remembers the write deadlines a handler asks the connection for.
type deadlineRecorder struct {
	gin.ResponseWriter
	asked *[]time.Time
}

func (w deadlineRecorder) SetWriteDeadline(t time.Time) error {
	*w.asked = append(*w.asked, t)
	return nil
}

// Installs, scans and package managers run for minutes behind these routes, and the server cuts a response that is not
// written within 30 seconds.
func TestSlowSidecarRoutesExtendTheWriteDeadline(t *testing.T) {
	var asked []time.Time
	engine := sidecarEngine(func(c *gin.Context) {
		c.Writer = deadlineRecorder{ResponseWriter: c.Writer, asked: &asked}
		c.Next()
	})
	for _, name := range []string{"tor", "psiphon", "naiveproxy", "wireproxy", "adguard"} {
		t.Run(name, func(t *testing.T) {
			asked = nil
			postTo(engine, "/panel/api/xray/"+name+"/bogus")
			if len(asked) != 1 || time.Until(asked[0]) < 5*time.Minute {
				t.Errorf("POST %s: the write deadline was asked for %v, want one deadline at least 5 minutes ahead", name, asked)
			}
		})
	}
}

// The deadline has to reach the connection through the gzip middleware the panel puts in front of every route.
func TestAllowSlowActionLetsARouteAnswerPastTheServerWriteTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(gzip.Gzip(gzip.DefaultCompression))
	writers := make(chan string, 2)
	late := func(c *gin.Context) {
		writers <- fmt.Sprintf("%T", c.Writer)
		time.Sleep(600 * time.Millisecond)
		c.JSON(http.StatusOK, gin.H{"done": true})
	}
	engine.POST("/slow", allowSlowAction, late)
	engine.POST("/plain", late)
	srv := httptest.NewUnstartedServer(engine)
	srv.Config.ReadTimeout = 200 * time.Millisecond
	srv.Config.WriteTimeout = 200 * time.Millisecond
	srv.Start()
	defer srv.Close()

	answer := func(path string) (string, error) {
		resp, err := http.Post(srv.URL+path, "text/plain", nil)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		return string(body), err
	}

	if body, err := answer("/plain"); err == nil {
		t.Fatalf("a route without allowSlowAction answered %q past the write timeout; the server no longer cuts it, so this test proves nothing", body)
	}
	body, err := answer("/slow")
	if err != nil || !strings.Contains(body, `"done":true`) {
		t.Errorf("a route with allowSlowAction answered %q, %v; want its response", body, err)
	}
	for range 2 {
		if writer := <-writers; !strings.Contains(writer, "gzip") {
			t.Errorf("a handler saw the writer %s, not the gzip one: this test no longer goes through the middleware it is about", writer)
		}
	}
}
