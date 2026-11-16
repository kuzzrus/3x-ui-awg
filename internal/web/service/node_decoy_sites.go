package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/frontproxy"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

const (
	// decoySitesFolder is the folder next to the database that holds the zips of the decoy sites.
	decoySitesFolder     = "decoy-sites"
	maxDecoySiteNameLen  = 100
	defaultDecoySiteName = "site.zip"
)

var (
	errDecoySiteMissing = errors.New("the decoy site is not on the panel: upload it again")
	errDecoySiteDamaged = errors.New("the decoy site on the panel is damaged: upload it again")
)

// DecoySite is an uploaded static site, a zip with an index.html, that agent nodes can serve as their decoy. A node
// names the site it serves by Sha256, the SHA-256 of the zip, which is also what the zip is called on disk.
type DecoySite struct {
	Sha256     string `json:"sha256" example:"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"`
	Name       string `json:"name" example:"blog.zip"`
	Size       int64  `json:"size" example:"183412"`
	UploadedAt int64  `json:"uploadedAt" example:"1700000000"` // unix seconds
}

// DecoySiteService keeps the sites that agent nodes serve as their decoy.
type DecoySiteService struct{}

func decoySitesDir() string               { return filepath.Join(config.GetDBFolderPath(), decoySitesFolder) }
func decoySiteZipPath(sha string) string  { return filepath.Join(decoySitesDir(), sha+".zip") }
func decoySiteMetaPath(sha string) string { return filepath.Join(decoySitesDir(), sha+".json") }

// decoySiteHeld reports whether the panel holds the zip of the site.
func decoySiteHeld(sha string) bool {
	if !agentproto.ValidDecoySite(sha) {
		return false
	}
	info, err := os.Stat(decoySiteZipPath(sha))
	return err == nil && info.Mode().IsRegular()
}

// checkedDecoySiteZip is the path of the zip of a site once its bytes are checked against its name, which a file that
// was changed or cut short on disk does not pass.
func checkedDecoySiteZip(sha string) (string, error) {
	if !decoySiteHeld(sha) {
		return "", errDecoySiteMissing
	}
	zip := decoySiteZipPath(sha)
	digest, err := decoyFileDigest(zip)
	if err != nil {
		return "", err
	}
	if digest != sha {
		return "", errDecoySiteDamaged
	}
	return zip, nil
}

func decoyFileDigest(file string) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// cleanDecoySiteName is the name of the file an upload came in, without the folders of the machine it came from.
func cleanDecoySiteName(name string) string {
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	name = clipRunes(strings.TrimSpace(name), maxDecoySiteNameLen)
	if name == "" || name == "." || name == "/" {
		return defaultDecoySiteName
	}
	return name
}

// storeDecoySiteFile writes what src gives to file through a temporary one, so that no reader sees half of it.
func storeDecoySiteFile(file string, src io.Reader) error {
	tmp, err := os.CreateTemp(filepath.Dir(file), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}

// readDecoySite is the record of a site that the panel holds the zip of.
func readDecoySite(sha string) (DecoySite, bool) {
	raw, err := os.ReadFile(decoySiteMetaPath(sha))
	if err != nil || !decoySiteHeld(sha) {
		return DecoySite{}, false
	}
	var site DecoySite
	if json.Unmarshal(raw, &site) != nil || site.Sha256 != sha {
		return DecoySite{}, false
	}
	return site, true
}

// Add keeps an uploaded site. The zip is unpacked once into a folder that is thrown away, which is the check the
// panel's own decoy upload makes: the size, the number of files, paths that stay inside, and an index.html. The same zip
// again is the site the panel holds already.
func (s *DecoySiteService) Add(name string, r io.ReaderAt, size int64) (*DecoySite, error) {
	dir := decoySitesDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	probe, err := os.MkdirTemp(dir, ".check-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(probe)
	if err := frontproxy.InstallDecoyArchive(filepath.Join(probe, "site"), r, size); err != nil {
		return nil, common.NewError(err.Error())
	}

	sum := sha256.New()
	if _, err := io.Copy(sum, io.NewSectionReader(r, 0, size)); err != nil {
		return nil, err
	}
	sha := hex.EncodeToString(sum.Sum(nil))
	if site, ok := readDecoySite(sha); ok {
		return &site, nil
	}
	if err := storeDecoySiteFile(decoySiteZipPath(sha), io.NewSectionReader(r, 0, size)); err != nil {
		return nil, err
	}
	site := DecoySite{Sha256: sha, Name: cleanDecoySiteName(name), Size: size, UploadedAt: time.Now().Unix()}
	raw, err := json.Marshal(site)
	if err != nil {
		return nil, err
	}
	if err := storeDecoySiteFile(decoySiteMetaPath(sha), strings.NewReader(string(raw))); err != nil {
		return nil, err
	}
	return &site, nil
}

// List is the sites the panel holds, the newest first.
func (s *DecoySiteService) List() ([]DecoySite, error) {
	entries, err := os.ReadDir(decoySitesDir())
	if errors.Is(err, fs.ErrNotExist) {
		return []DecoySite{}, nil
	}
	if err != nil {
		return nil, err
	}
	sites := []DecoySite{}
	for _, entry := range entries {
		sha, isRecord := strings.CutSuffix(entry.Name(), ".json")
		if !isRecord || !agentproto.ValidDecoySite(sha) {
			continue
		}
		if site, ok := readDecoySite(sha); ok {
			sites = append(sites, site)
		}
	}
	slices.SortFunc(sites, func(a, b DecoySite) int {
		if a.UploadedAt != b.UploadedAt {
			return int(b.UploadedAt - a.UploadedAt)
		}
		return strings.Compare(a.Name, b.Name)
	})
	return sites, nil
}

// decoySiteUsers are the names of the nodes that serve the site.
func decoySiteUsers(sha string) ([]string, error) {
	var nodes []model.Node
	if err := database.GetDB().Where("kind = ?", model.NodeKindAgent).Find(&nodes).Error; err != nil {
		return nil, err
	}
	var names []string
	for _, n := range nodes {
		if n.Decoy.Mode == agentproto.DecoyUpload && n.Decoy.Site == sha {
			names = append(names, n.Name)
		}
	}
	return names, nil
}

// Delete removes a site, unless a node serves it: the node would be left with a decoy that the panel can no longer send.
func (s *DecoySiteService) Delete(sha string) error {
	if !agentproto.ValidDecoySite(sha) {
		return common.NewError("unknown decoy site")
	}
	users, err := decoySiteUsers(sha)
	if err != nil {
		return err
	}
	if len(users) > 0 {
		return common.NewErrorf("the site is the decoy of %s: give them another decoy first", strings.Join(users, ", "))
	}
	for _, file := range []string{decoySiteZipPath(sha), decoySiteMetaPath(sha)} {
		if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}
