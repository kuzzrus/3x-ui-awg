package hostaction

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// placeSnippet writes the staged content beside the target and renames it over the target, so that whatever reads the
// target (a scheduler running a script) never sees half of it, and leaves nothing beside it when the rename fails.
const placeSnippet = `dst=$1 mode=$2
tmp=$(mktemp "$dst.new.XXXXXX") || exit 1
trap 'rm -f "$tmp"' EXIT
cat "$HOSTACTION_DIR/stage" >"$tmp" || exit 1
chmod "$mode" "$tmp" || exit 1
sync "$tmp" 2>/dev/null
mv -fT "$tmp" "$dst" || exit 1
trap - EXIT
`

// WriteFile replaces the file at path with content, outside the panel's sandbox, in one rename.
func WriteFile(ctx context.Context, path string, content []byte, perm os.FileMode) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("hostaction: %s is not an absolute path", path)
	}
	if content == nil {
		content = []byte{}
	}
	_, err := Run(ctx, Spec{
		Name:  "write " + path,
		Argv:  []string{"/bin/bash", "-c", placeSnippet, "hostaction-write", path, fmt.Sprintf("%04o", perm.Perm())},
		Stage: content,
	})
	return err
}

// Remove deletes the file at path, outside the panel's sandbox; a file that is not there is no error.
func Remove(ctx context.Context, path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("hostaction: %s is not an absolute path", path)
	}
	_, err := Run(ctx, Spec{Name: "remove " + path, Argv: []string{"rm", "-f", "--", path}})
	return err
}
