package service

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// decoySiteZip is the zip of a site, the files in a fixed order so that the same files make the same zip.
func decoySiteZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, name := range slices.Sorted(maps.Keys(files)) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func zipDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// newDecoySiteLibrary is a panel with a database and a folder of its own for the decoy sites.
func newDecoySiteLibrary(t *testing.T) *DecoySiteService {
	t.Helper()
	setupSettingTestDB(t)
	t.Setenv("XUI_DB_FOLDER", t.TempDir())
	return &DecoySiteService{}
}

func addDecoySite(t *testing.T, svc *DecoySiteService, name string, files map[string]string) *DecoySite {
	t.Helper()
	raw := decoySiteZip(t, files)
	site, err := svc.Add(name, bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("Add(%q): %v", name, err)
	}
	return site
}

var homePage = map[string]string{"index.html": "<html>home</html>"}

func TestDecoySiteServiceKeepsAnUploadUnderTheDigestOfItsZip(t *testing.T) {
	svc := newDecoySiteLibrary(t)
	raw := decoySiteZip(t, map[string]string{"index.html": "<html>home</html>", "css/site.css": "body{}"})

	site, err := svc.Add(`C:\fakepath\blog.zip`, bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if site.Sha256 != zipDigest(raw) || site.Name != "blog.zip" || site.Size != int64(len(raw)) || site.UploadedAt == 0 {
		t.Fatalf("site = %+v, want the digest and size of the zip, the name of the file without its folders, and a time", site)
	}
	kept, err := os.ReadFile(decoySiteZipPath(site.Sha256))
	if err != nil || !bytes.Equal(kept, raw) {
		t.Fatalf("the zip on disk = %d bytes, %v, want the upload as it came", len(kept), err)
	}
	entries, err := os.ReadDir(decoySitesDir())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if want := []string{site.Sha256 + ".json", site.Sha256 + ".zip"}; !slices.Equal(names, want) {
		t.Fatalf("the folder holds %v, want %v and no staging folder or temporary file left over", names, want)
	}
	if !decoySiteHeld(site.Sha256) {
		t.Fatal("the panel does not hold the site it was just given")
	}
}

func TestDecoySiteServiceTakesTheSameZipOnlyOnce(t *testing.T) {
	svc := newDecoySiteLibrary(t)
	first := addDecoySite(t, svc, "blog.zip", homePage)
	again := addDecoySite(t, svc, "renamed.zip", homePage)

	if again.Sha256 != first.Sha256 || again.Name != "blog.zip" {
		t.Fatalf("the same zip again = %+v, want the site that is kept already, under its first name", again)
	}
	if sites, err := svc.List(); err != nil || len(sites) != 1 {
		t.Fatalf("List = %v, %v, want the one site", sites, err)
	}
}

func TestDecoySiteServiceRefusesWhatIsNotASite(t *testing.T) {
	escaping := decoySiteZip(t, map[string]string{"index.html": "<html>home</html>", "../outside.txt": "x"})
	noIndex := decoySiteZip(t, map[string]string{"about.html": "<html>about</html>"})
	for name, tc := range map[string]struct {
		raw  []byte
		want string
	}{
		"a file that is no zip":     {[]byte("not a zip"), "not a readable zip"},
		"a zip with no index page":  {noIndex, "no index.html"},
		"a zip with a path outside": {escaping, "outside"},
		"an empty upload":           {nil, "not a readable zip"},
	} {
		t.Run(name, func(t *testing.T) {
			svc := newDecoySiteLibrary(t)
			if site, err := svc.Add("x.zip", bytes.NewReader(tc.raw), int64(len(tc.raw))); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Add = %+v, %v, want an error that says %q", site, err, tc.want)
			}
			if sites, err := svc.List(); err != nil || len(sites) != 0 {
				t.Fatalf("List = %v, %v after a refusal, want none", sites, err)
			}
			entries, _ := os.ReadDir(decoySitesDir())
			if len(entries) != 0 {
				t.Fatalf("the folder holds %v after a refusal, want nothing, not even the folder the zip was tried in", entries)
			}
		})
	}
}

func TestDecoySiteServiceListsTheNewestFirst(t *testing.T) {
	svc := newDecoySiteLibrary(t)
	older := addDecoySite(t, svc, "b.zip", map[string]string{"index.html": "b"})
	newer := addDecoySite(t, svc, "a.zip", map[string]string{"index.html": "a"})
	tied := addDecoySite(t, svc, "c.zip", map[string]string{"index.html": "c"})
	for site, at := range map[*DecoySite]int64{older: 1000, newer: 3000, tied: 3000} {
		site.UploadedAt = at
		raw, err := json.Marshal(site)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(decoySiteMetaPath(site.Sha256), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	sites, err := svc.List()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, site := range sites {
		names = append(names, site.Name)
	}
	if want := []string{"a.zip", "c.zip", "b.zip"}; !slices.Equal(names, want) {
		t.Fatalf("List = %v, want the newest first and a tie by name: %v", names, want)
	}
}

func TestDecoySiteServiceListsNothingBeforeTheFirstUpload(t *testing.T) {
	svc := newDecoySiteLibrary(t)
	sites, err := svc.List()
	if err != nil || sites == nil || len(sites) != 0 {
		t.Fatalf("List = %#v, %v, want an empty list, which the page shows as no sites rather than an error", sites, err)
	}
}

func TestDecoySiteServiceDeleteRefusesASiteANodeServes(t *testing.T) {
	svc := newDecoySiteLibrary(t)
	site := addDecoySite(t, svc, "blog.zip", homePage)
	node := &model.Node{
		Name: "edge-1", Kind: model.NodeKindAgent, Address: "203.0.113.7", Port: 8443,
		Decoy: model.NodeDecoy{Mode: agentproto.DecoyUpload, Site: site.Sha256, Domain: "edge.example"},
	}
	if err := database.GetDB().Create(node).Error; err != nil {
		t.Fatal(err)
	}

	if err := svc.Delete(site.Sha256); err == nil || !strings.Contains(err.Error(), "edge-1") {
		t.Fatalf("Delete = %v, want a refusal that names the node that serves the site", err)
	}
	if !decoySiteHeld(site.Sha256) {
		t.Fatal("the site was removed although a node serves it")
	}

	other, err := json.Marshal(model.NodeDecoy{Mode: agentproto.DecoyTemplate, Template: "parked", Domain: "edge.example"})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Model(node).Update("decoy", string(other)).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(site.Sha256); err != nil {
		t.Fatalf("Delete after the node serves another decoy: %v", err)
	}
	if entries, _ := os.ReadDir(decoySitesDir()); len(entries) != 0 {
		t.Fatalf("the folder holds %v after the delete, want nothing", entries)
	}
	if err := svc.Delete(site.Sha256); err != nil {
		t.Fatalf("Delete of a site that is gone: %v, want no complaint", err)
	}
	if err := svc.Delete("../" + site.Sha256[:61]); err == nil {
		t.Fatal("Delete took a name that is no digest")
	}
}

// A node of another kind that carries a decoy in its row does not serve it, so it does not hold a site back.
func TestDecoySiteServiceDeleteIgnoresNodesThatAreNotAgents(t *testing.T) {
	svc := newDecoySiteLibrary(t)
	site := addDecoySite(t, svc, "blog.zip", homePage)
	node := &model.Node{Name: "panel-node", Address: "203.0.113.8", Port: 2053, Decoy: model.NodeDecoy{Mode: agentproto.DecoyUpload, Site: site.Sha256}}
	if err := database.GetDB().Create(node).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(site.Sha256); err != nil {
		t.Fatalf("Delete = %v, want the site removed", err)
	}
}

func TestCheckedDecoySiteZipRefusesAMissingAndADamagedFile(t *testing.T) {
	svc := newDecoySiteLibrary(t)
	site := addDecoySite(t, svc, "blog.zip", homePage)

	if path, err := checkedDecoySiteZip(site.Sha256); err != nil || path != decoySiteZipPath(site.Sha256) {
		t.Fatalf("checkedDecoySiteZip = %q, %v, want the path of the zip", path, err)
	}
	if err := os.WriteFile(decoySiteZipPath(site.Sha256), []byte("cut short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := checkedDecoySiteZip(site.Sha256); !errors.Is(err, errDecoySiteDamaged) {
		t.Fatalf("a changed file: %v, want %v", err, errDecoySiteDamaged)
	}
	if err := os.Remove(decoySiteZipPath(site.Sha256)); err != nil {
		t.Fatal(err)
	}
	if _, err := checkedDecoySiteZip(site.Sha256); !errors.Is(err, errDecoySiteMissing) {
		t.Fatalf("a missing file: %v, want %v", err, errDecoySiteMissing)
	}
	if _, err := checkedDecoySiteZip("../../etc/passwd"); !errors.Is(err, errDecoySiteMissing) {
		t.Fatalf("a name that is no digest: %v, want it treated as a site that is not there", err)
	}
}

func TestCheckedNodeDecoyWantsTheSiteOnThePanel(t *testing.T) {
	svc := newDecoySiteLibrary(t)
	site := addDecoySite(t, svc, "blog.zip", homePage)
	upload := func(digest string) model.NodeDecoy {
		return model.NodeDecoy{Mode: agentproto.DecoyUpload, Site: digest, Domain: "edge.example"}
	}

	got, err := checkedNodeDecoy(upload(strings.ToUpper(site.Sha256)))
	if err != nil || got.Site != site.Sha256 || got.Mode != agentproto.DecoyUpload {
		t.Fatalf("checkedNodeDecoy of a site the panel holds = %+v, %v, want it normalized", got, err)
	}
	if _, err := checkedNodeDecoy(upload(strings.Repeat("a", 64))); err == nil || !strings.Contains(err.Error(), "upload it again") {
		t.Fatalf("checkedNodeDecoy of a site the panel does not hold = %v, want a refusal", err)
	}
	if _, err := checkedNodeDecoy(upload("")); err == nil {
		t.Fatal("an upload decoy that names no site was taken")
	}
	// What the sync reads is not asked to hold the site: a missing one is told as a problem of the node there.
	if _, err := normalizeNodeDecoy(upload(strings.Repeat("a", 64))); err != nil {
		t.Fatalf("normalizeNodeDecoy = %v, want only the form of the name checked", err)
	}
}

func TestCleanDecoySiteName(t *testing.T) {
	long := strings.Repeat("я", maxDecoySiteNameLen+20) + ".zip"
	for in, want := range map[string]string{
		"blog.zip":                  "blog.zip",
		`C:\Users\me\Desktop\a.zip`: "a.zip",
		"/home/me/b.zip":            "b.zip",
		"  spaced name.zip  ":       "spaced name.zip",
		"tab\there\nnewline.zip":    "tabherenewline.zip",
		"":                          defaultDecoySiteName,
		"   ":                       defaultDecoySiteName,
		"..":                        "..",
		"/":                         defaultDecoySiteName,
		"dir/":                      "dir",
		long:                        strings.Repeat("я", maxDecoySiteNameLen),
	} {
		if got := cleanDecoySiteName(in); got != want {
			t.Errorf("cleanDecoySiteName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStoreDecoySiteFileLeavesNoHalfWrittenFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "x.zip")
	if err := storeDecoySiteFile(target, strings.NewReader("first")); err != nil {
		t.Fatal(err)
	}
	failing := &failingReader{after: "second half", err: os.ErrClosed}
	if err := storeDecoySiteFile(target, failing); err == nil {
		t.Fatal("a source that failed was taken")
	}
	if got, _ := os.ReadFile(target); string(got) != "first" {
		t.Fatalf("the file holds %q after a failed write, want the content from before", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("the folder holds %v, want only the file, with no temporary one left", entries)
	}
}

// failingReader gives some bytes and then fails.
type failingReader struct {
	after string
	err   error
	done  bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, r.err
	}
	r.done = true
	return copy(p, r.after), nil
}
